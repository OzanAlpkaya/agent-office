package role

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

// builtinFS holds the roles shipped with agent-office, one directory each.
//
//go:embed all:builtin
var builtinFS embed.FS

// DirEnv overrides the user roles directory.
const DirEnv = "AGENT_OFFICE_ROLES_DIR"

// Names of the built-in roles the engine starts by itself, one per behaviour
// (a user role with the same name replaces it).
const (
	NameArchitect = "architect"
	NameTechLead  = "tech-lead"
	NameWorker    = "worker"
	NameQA        = "qa"
	NameReviewer  = "reviewer"
	NameAssistant = "assistant"
)

// DefaultName is the role the engine uses for a behaviour when nobody names one.
func DefaultName(b Behaviour) string {
	switch b {
	case Architect:
		return NameArchitect
	case Lead:
		return NameTechLead
	case QA:
		return NameQA
	case Reviewer:
		return NameReviewer
	case Assistant:
		return NameAssistant
	}
	return NameWorker
}

// DefaultDir is the user roles directory: AGENT_OFFICE_ROLES_DIR, or
// $XDG_CONFIG_HOME/agent-office/roles (~/.config/agent-office/roles).
func DefaultDir() (string, error) {
	if d := os.Getenv(DirEnv); d != "" {
		return filepath.Abs(d)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("role: %w", err)
	}
	return filepath.Join(cfg, "agent-office", "roles"), nil
}

// Entry is one role found by List, valid or not.
type Entry struct {
	Name     string
	Source   string // SourceBuiltin or the role directory
	Role     *Role  // nil if Err is set
	Err      error
	Replaces bool // a user role replacing the built-in of the same name
}

// Builtins returns the built-in roles, validated like user roles.
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
		r, err := load(sub, d.Name(), SourceBuiltin, "builtin:"+d.Name()+"/"+File)
		out = append(out, Entry{Name: d.Name(), Source: SourceBuiltin, Role: r, Err: err})
	}
	return out
}

// LoadDir loads the role in directory dir (named like the role). Every file
// and directory in it must belong to you and not be writable by group or
// others, and none may be a symlink.
func LoadDir(dir string) (*Role, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	label := filepath.Join(dir, File)
	probs := checkTree(dir)
	r, err := load(os.DirFS(dir), filepath.Base(dir), dir, label)
	if len(probs) == 0 {
		return r, err
	}
	var re *Error
	if errors.As(err, &re) {
		probs = append(probs, re.Problems...)
	} else if err != nil {
		probs = append(probs, Problem{Msg: err.Error()})
	}
	return nil, &Error{File: label, Problems: probs}
}

// List returns the built-in roles and those in userDir (which may not exist),
// sorted by name. A user role with a built-in's name comes after it, marked
// Replaces; one that changes the built-in's behaviour carries an error.
func List(userDir string) ([]Entry, error) {
	out := Builtins()
	builtin := map[string]Behaviour{}
	for _, b := range out {
		if b.Role != nil {
			builtin[b.Name] = b.Role.Behaviour
		}
	}
	user, err := userEntries(userDir)
	for i := range user {
		u := &user[i]
		if b, ok := builtin[u.Name]; ok {
			u.Replaces = true
			if u.Role != nil && u.Role.Behaviour != b {
				u.Err = fmt.Errorf("%s: a role replacing the built-in %s must keep its behaviour %s (agent-office starts it "+
					"for that job); give your role another name", u.Source, u.Name, b)
				u.Role = nil
			}
		}
	}
	out = append(out, user...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
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
		return nil, fmt.Errorf("role: %w", err)
	}
	if probs := checkPerm(userDir, "the roles directory"); len(probs) > 0 {
		return nil, &Error{File: userDir, Problems: probs}
	}
	var out []Entry
	for _, d := range dirs {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		dir := filepath.Join(userDir, d.Name())
		e := Entry{Name: d.Name(), Source: dir}
		if !d.IsDir() {
			e.Err = fmt.Errorf("%s: not a role directory (a role is a directory with a %s); move the file elsewhere", dir, File)
		} else {
			e.Role, e.Err = LoadDir(dir)
		}
		out = append(out, e)
	}
	return out, nil
}

// Set is the roles of one daemon run, by name.
type Set struct {
	roles map[string]*Role
	names []string
}

// Load returns the built-in roles with the valid user roles of userDir on top.
// Any invalid role is an error: a role the user wrote should not be skipped
// silently.
func Load(userDir string) (*Set, error) {
	all, err := List(userDir)
	if err != nil {
		return nil, err
	}
	s := &Set{roles: map[string]*Role{}}
	var errs []error
	for _, e := range all {
		if e.Err != nil {
			errs = append(errs, e.Err)
			continue
		}
		if _, seen := s.roles[e.Name]; !seen {
			s.names = append(s.names, e.Name)
		}
		s.roles[e.Name] = e.Role // a user role comes after the built-in it replaces
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, b := range Behaviours {
		if r := s.roles[DefaultName(b)]; r == nil || r.Behaviour != b {
			return nil, fmt.Errorf("role: the built-in role %s is missing", DefaultName(b))
		}
	}
	return s, nil
}

// MustBuiltins returns the built-in roles only. It panics if they are invalid,
// which a test rules out.
func MustBuiltins() *Set {
	s, err := Load("")
	if err != nil {
		panic(err)
	}
	return s
}

// Get returns the role called name.
func (s *Set) Get(name string) (*Role, bool) {
	r, ok := s.roles[name]
	return r, ok
}

// Default returns the role the engine uses for behaviour b.
func (s *Set) Default(b Behaviour) *Role { return s.roles[DefaultName(b)] }

// List returns every role, sorted by name.
func (s *Set) List() []*Role {
	out := make([]*Role, 0, len(s.names))
	for _, n := range s.names {
		out = append(out, s.roles[n])
	}
	return out
}

// WithModels returns a copy of s whose roles named in models use that model
// (--role-model). Every name must be a role of s.
func (s *Set) WithModels(models map[string]string) (*Set, error) {
	out := &Set{roles: map[string]*Role{}, names: s.names}
	for n, r := range s.roles {
		c := *r
		out.roles[n] = &c
	}
	for n, m := range models {
		r, ok := out.roles[n]
		if !ok {
			return nil, fmt.Errorf("--role-model: unknown role %q (run `agent-office role list`)", n)
		}
		if m == "" || len(m) > maxModel || !modelRe.MatchString(m) {
			return nil, fmt.Errorf("--role-model: %q is not a model name for role %s", m, n)
		}
		r.Model = m
	}
	return out, nil
}

// ParseModels parses "role=model,role=model".
func ParseModels(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, m, ok := strings.Cut(part, "=")
		n, m = strings.TrimSpace(n), strings.TrimSpace(m)
		if !ok || n == "" || m == "" {
			return nil, fmt.Errorf("--role-model: %q must be role=model, e.g. architect=opus", part)
		}
		if _, dup := out[n]; dup {
			return nil, fmt.Errorf("--role-model: role %s is listed twice", n)
		}
		out[n] = m
	}
	return out, nil
}

// checkTree checks ownership and modes of everything under root.
func checkTree(root string) []Problem {
	var probs []Problem
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n++; n > 50 {
			return fmt.Errorf("more than 50 entries; a role directory holds a %s", File)
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			probs = append(probs, Problem{Field: rel, Msg: "is a symlink; a role may hold only regular files and directories"})
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			probs = append(probs, Problem{Field: rel, Msg: "is not a regular file"})
			return nil
		}
		what := rel
		if rel == "." {
			what = "the role directory"
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
		return []Problem{{Field: what, Msg: "is not owned by you; a role must belong to the user running agent-office"}}
	case fi.Mode().Perm()&0o022 != 0:
		return []Problem{{Field: what, Msg: fmt.Sprintf("is writable by group or others (chmod go-w %s)", p)}}
	}
	return nil
}
