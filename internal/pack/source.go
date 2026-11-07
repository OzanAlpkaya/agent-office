package pack

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// builtinFS holds the packs shipped with agent-office, one directory each.
//
//go:embed all:builtin
var builtinFS embed.FS

// DirEnv overrides the user packs directory.
const DirEnv = "AGENT_OFFICE_PACKS_DIR"

// DefaultDir is the user packs directory: AGENT_OFFICE_PACKS_DIR, or
// $XDG_CONFIG_HOME/agent-office/packs (~/.config/agent-office/packs).
func DefaultDir() (string, error) {
	if d := os.Getenv(DirEnv); d != "" {
		return filepath.Abs(d)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("pack: %w", err)
	}
	return filepath.Join(cfg, "agent-office", "packs"), nil
}

// Entry is one pack found by List, valid or not.
type Entry struct {
	Name   string
	Source string // SourceBuiltin or the pack directory
	Pack   *Pack  // nil if Err is set
	Err    error
}

// Builtins returns the built-in packs. They are validated like user packs;
// tests make sure they are valid.
func Builtins() []Entry {
	dirs, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return []Entry{{Name: "builtin", Source: SourceBuiltin, Err: err}}
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub, err := fs.Sub(builtinFS, path.Join("builtin", d.Name()))
		if err != nil {
			out = append(out, Entry{Name: d.Name(), Source: SourceBuiltin, Err: err})
			continue
		}
		p, err := load(sub, d.Name(), SourceBuiltin, "builtin:"+d.Name()+"/"+File)
		out = append(out, Entry{Name: d.Name(), Source: SourceBuiltin, Pack: p, Err: err})
	}
	return out
}

// LoadDir loads the pack in directory dir (named like the pack). Every file
// and directory in it must belong to you and not be writable by group or
// others, and none may be a symlink: a pack decides what leaves the sandbox.
func LoadDir(dir string) (*Pack, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	label := filepath.Join(dir, File)
	probs := checkTree(dir)
	p, err := load(os.DirFS(dir), filepath.Base(dir), dir, label)
	if len(probs) == 0 {
		return p, err
	}
	var pe *Error
	if errors.As(err, &pe) {
		probs = append(probs, pe.Problems...)
	} else if err != nil {
		probs = append(probs, Problem{Msg: err.Error()})
	}
	return nil, &Error{File: label, Problems: probs}
}

// List returns the built-in packs and those in userDir (which may not
// exist), sorted by name; packs that fail validation carry their error.
func List(userDir string) ([]Entry, error) {
	out := Builtins()
	user, err := userEntries(userDir)
	if err != nil {
		return out, err
	}
	out = append(out, user...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func userEntries(userDir string) ([]Entry, error) {
	if userDir == "" {
		return nil, nil
	}
	dirs, err := os.ReadDir(userDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if probs := checkPerm(userDir, "the packs directory"); len(probs) > 0 {
		return nil, &Error{File: userDir, Problems: probs}
	}
	builtin := map[string]bool{}
	for _, b := range Builtins() {
		builtin[b.Name] = true
	}
	var out []Entry
	for _, d := range dirs {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		dir := filepath.Join(userDir, d.Name())
		e := Entry{Name: d.Name(), Source: dir}
		switch {
		case !d.IsDir():
			e.Err = fmt.Errorf("%s: not a pack directory (a pack is a directory with a %s); move the file elsewhere", dir, File)
		case builtin[d.Name()]:
			e.Err = fmt.Errorf("%s: a user pack may not reuse the built-in name %q; rename the directory and its name field", dir, d.Name())
		default:
			e.Pack, e.Err = LoadDir(dir)
		}
		out = append(out, e)
	}
	return out, nil
}

// Find returns the pack called name: a built-in or one in userDir.
func Find(name, userDir string) (*Pack, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("pack name %q is invalid: lower-case letters, digits and '-', starting with a letter", name)
	}
	all, err := List(userDir)
	var found []Entry
	var names []string
	for _, e := range all {
		if e.Name == name {
			found = append(found, e)
		}
		names = append(names, e.Name)
	}
	if len(found) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unknown stack pack %q; available: %s (run `agent-office pack list`; user packs go in %s)",
			name, strings.Join(names, ", "), userDir)
	}
	if found[0].Err != nil {
		return nil, found[0].Err
	}
	for _, e := range found[1:] { // a user pack shadowing a built-in carries an error
		if e.Err != nil {
			return nil, e.Err
		}
	}
	return found[0].Pack, nil
}

// checkTree checks ownership and modes of everything under root.
func checkTree(root string) []Problem {
	var probs []Problem
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n++; n > maxFiles*2 {
			return fmt.Errorf("more than %d entries; a pack holds a pack.yaml, a Containerfile and its build files, and skills", maxFiles*2)
		}
		rel, _ := filepath.Rel(root, p)
		if d.Type()&fs.ModeSymlink != 0 {
			probs = append(probs, Problem{Field: rel, Msg: "is a symlink; a pack may hold only regular files and directories"})
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			probs = append(probs, Problem{Field: rel, Msg: "is not a regular file"})
			return nil
		}
		what := rel
		if rel == "." {
			what = "the pack directory"
		}
		probs = append(probs, checkPerm(p, what)...)
		return nil
	})
	if err != nil {
		probs = append(probs, Problem{Msg: err.Error()})
	}
	return probs
}

func checkPerm(p, what string) []Problem {
	fi, err := os.Lstat(p)
	if err != nil {
		return []Problem{{Field: what, Msg: err.Error()}}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok || st.Uid != uint32(os.Getuid()):
		return []Problem{{Field: what, Msg: "is not owned by you; a pack must belong to the user running agent-office"}}
	case fi.Mode().Perm()&0o022 != 0:
		return []Problem{{Field: what, Msg: fmt.Sprintf("is writable by group or others (chmod go-w %s)", p)}}
	}
	return nil
}

// BuildFiles returns the image build context: every file of the pack except
// pack.yaml and skills/, by path. Nil for a pack without a Containerfile.
func (p *Pack) BuildFiles() (map[string][]byte, error) {
	if p.Containerfile == "" {
		return nil, nil
	}
	files := map[string][]byte{}
	var total int64
	err := fs.WalkDir(p.fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "skills" && d.IsDir() {
			return fs.SkipDir
		}
		if d.IsDir() || name == File {
			return nil
		}
		b, err := readFile(p.fsys, name, maxBuildBytes)
		if err != nil {
			return err
		}
		if total += int64(len(b)); total > maxBuildBytes || len(files) >= maxFiles {
			return fmt.Errorf("the build files are larger than %d MiB or more than %d files", maxBuildBytes>>20, maxFiles)
		}
		files[name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// CopySkills writes the pack's skills into dst/<skill>/ (an agent's
// CLAUDE_CONFIG_DIR/skills, ADR 0023).
func (p *Pack) CopySkills(dst string) error {
	for _, s := range p.Skills {
		err := fs.WalkDir(p.fsys, path.Join("skills", s), func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			target := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(name, "skills/")))
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			b, err := readFile(p.fsys, name, maxSkillBytes)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, 0o644)
		})
		if err != nil {
			return fmt.Errorf("pack %s: copy skill %s: %w", p.Name, s, err)
		}
	}
	return nil
}
