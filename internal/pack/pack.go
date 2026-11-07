// Package pack loads and validates stack packs (ADR 0023): declarative YAML
// that configures a project's sandboxed agents (worker image, egress
// allowlist, secret env var names, Claude Code skills and MCP servers).
//
// Packs come from the binary (built-ins) or from a directory the user owns,
// never from the repository the agents work on.
package pack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/OzanAlpkaya/agent-office/internal/sandbox"
)

// Schema is the pack format version this binary reads.
const Schema = 1

// File is the pack definition inside a pack directory.
const File = "pack.yaml"

// Limits on pack contents. They are generous for real packs and keep a
// mistaken pack (a build directory copied in, say) from being loaded.
const (
	maxPackBytes          = 64 << 10
	maxContainerfileBytes = 64 << 10
	maxSkillMDBytes       = 64 << 10
	maxSkillBytes         = 1 << 20
	maxBuildBytes         = 16 << 20
	maxFiles              = 200
	maxHosts              = 64
	maxEnv                = 32
	maxSkills             = 16
	maxServers            = 16
	maxArgs               = 32
	maxDescription        = 300
)

var (
	nameRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	envNameRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	serverRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	unknownRe   = regexp.MustCompile(`field (\S+) not found in type \S+`)
)

// reservedServers are MCP server names agent-office or Claude Code use
// (code.claude.com/docs/en/mcp).
var reservedServers = map[string]bool{"agent-office": true, "workspace": true, "claude-in-chrome": true,
	"computer-use": true}

// Pack is a validated stack pack.
type Pack struct {
	Name          string
	Description   string
	Source        string // SourceBuiltin or the pack directory
	Containerfile string // path inside the pack; empty: the default image
	Egress        []string
	Env           []EnvVar
	Skills        []string
	MCPServers    map[string]MCPServer

	fsys   fs.FS             // the pack directory
	skills map[string][]byte // skill files read and checked at load, by path under skills/
}

// SourceBuiltin is Pack.Source for packs embedded in the binary.
const SourceBuiltin = "builtin"

// EnvVar is a secret the pack's agents get, by name (ADR 0023). Its value
// is a user-only file in the secrets directory.
type EnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
}

// MCPServer is an extra MCP server: a stdio command inside the worker or an
// HTTPS URL reached through the egress proxy.
type MCPServer struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
}

// ServerNames returns the MCP server names in order.
func (p *Pack) ServerNames() []string {
	names := make([]string, 0, len(p.MCPServers))
	for n := range p.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Problem is one validation finding: the field and what to do about it.
type Problem struct {
	Field string
	Msg   string
}

// Error lists every problem found in one pack.
type Error struct {
	File     string // e.g. ~/.config/agent-office/packs/go/pack.yaml or builtin:go/pack.yaml
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	n := len(e.Problems)
	fmt.Fprintf(&b, "%s: invalid stack pack (%d problem%s, see docs/packs.md):", e.File, n, map[bool]string{true: "s"}[n != 1])
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		if p.Field != "" {
			b.WriteString(p.Field + ": ")
		}
		b.WriteString(p.Msg)
	}
	return b.String()
}

func (e *Error) add(field, format string, a ...any) {
	e.Problems = append(e.Problems, Problem{Field: field, Msg: fmt.Sprintf(format, a...)})
}

// file is pack.yaml as written; decoding is strict (unknown fields fail).
type file struct {
	Schema      *int   `yaml:"schema"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Toolchain   *struct {
		Containerfile string `yaml:"containerfile"`
	} `yaml:"toolchain"`
	Egress *struct {
		Allow []string `yaml:"allow"`
	} `yaml:"egress"`
	Env []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Required    bool   `yaml:"required"`
	} `yaml:"env"`
	Skills     []string `yaml:"skills"`
	MCPServers map[string]struct {
		Command string   `yaml:"command"`
		Args    []string `yaml:"args"`
		URL     string   `yaml:"url"`
	} `yaml:"mcp_servers"`
}

// load reads and validates the pack in fsys (the pack directory). dir is the
// directory's name, which must equal the pack's name; label names the file
// in errors.
func load(fsys fs.FS, dir, source, label string) (*Pack, error) {
	e := &Error{File: label}
	raw, err := readFile(fsys, File, maxPackBytes)
	if err != nil {
		e.add("", "%v", err)
		return nil, e
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			e.add("", "the file is empty; start with `schema: 1` and `name: %s`", dir)
		} else {
			yamlProblems(e, err)
		}
		return nil, e
	}
	var more any
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		e.add("", "more than one YAML document (a second ---); a pack is a single document")
	}

	p := &Pack{Source: source, fsys: fsys, MCPServers: map[string]MCPServer{}, skills: map[string][]byte{}}
	switch {
	case f.Schema == nil:
		e.add("schema", "missing; add `schema: %d` as the first line", Schema)
	case *f.Schema != Schema:
		e.add("schema", "version %d is not supported by this agent-office (it reads schema %d)", *f.Schema, Schema)
	}
	switch p.Name = f.Name; {
	case p.Name == "":
		e.add("name", "missing; add `name: %s` (the directory name)", dir)
	case !nameRe.MatchString(p.Name):
		e.add("name", "%q must be lower-case letters, digits and '-', start with a letter and be at most 32 characters", p.Name)
	case p.Name != dir:
		e.add("name", "%q does not match the pack directory %q; rename one of them", p.Name, dir)
	}
	p.Description = strings.TrimSpace(f.Description)
	if msg := oneLine(p.Description, maxDescription, true); msg != "" {
		e.add("description", "%s", msg)
	}

	if f.Toolchain != nil {
		p.Containerfile = f.Toolchain.Containerfile
		if p.Containerfile == "" {
			e.add("toolchain.containerfile", "missing; name the Containerfile in the pack directory, or remove `toolchain` to use the default image")
		} else if msg := relPath(p.Containerfile); msg != "" {
			e.add("toolchain.containerfile", "%s", msg)
		} else if src, err := readFile(fsys, p.Containerfile, maxContainerfileBytes); err != nil {
			e.add("toolchain.containerfile", "%v", err)
		} else if msg := checkContainerfile(src); msg != "" {
			e.add("toolchain.containerfile", "%s", msg)
		}
	}

	var valid []string
	if f.Egress != nil {
		if len(f.Egress.Allow) > maxHosts {
			e.add("egress.allow", "%d hosts; at most %d", len(f.Egress.Allow), maxHosts)
		}
		seen := map[string]bool{}
		for i, h := range f.Egress.Allow {
			field := fmt.Sprintf("egress.allow[%d]", i)
			if _, err := sandbox.ParseAllowlist([]string{h}); err != nil || strings.TrimSpace(h) != h || h == "" {
				e.add(field, "%q is not a host name; use a lower-case name such as example.com, or .example.com for its subdomains (no scheme, port, IP address or *)", h)
				continue
			}
			if seen[h] {
				e.add(field, "%q is listed twice", h)
				continue
			}
			seen[h] = true
			valid = append(valid, h)
		}
	}
	p.Egress = valid
	allow, _ := sandbox.ParseAllowlist(valid)

	if len(f.Env) > maxEnv {
		e.add("env", "%d variables; at most %d", len(f.Env), maxEnv)
	}
	seenEnv := map[string]bool{}
	for i, v := range f.Env {
		field := fmt.Sprintf("env[%d].name", i)
		switch {
		case v.Name == "":
			e.add(field, "missing")
		case !envNameRe.MatchString(v.Name):
			e.add(field, "%q must be upper-case letters, digits and '_', starting with a letter", v.Name)
		case sandbox.ReservedEnv(v.Name):
			e.add(field, "%s is reserved (agent-office, the sandbox or Claude Code set it, or it would hand agents a credential they must not hold); choose another name", v.Name)
		case seenEnv[v.Name]:
			e.add(field, "%s is listed twice", v.Name)
		default:
			seenEnv[v.Name] = true
			d := strings.TrimSpace(v.Description)
			if msg := oneLine(d, 200, false); msg != "" {
				e.add(fmt.Sprintf("env[%d].description", i), "%s", msg)
			}
			p.Env = append(p.Env, EnvVar{Name: v.Name, Description: d, Required: v.Required})
		}
	}

	if len(f.Skills) > maxSkills {
		e.add("skills", "%d skills; at most %d", len(f.Skills), maxSkills)
	}
	for i, s := range f.Skills {
		field := fmt.Sprintf("skills[%d]", i)
		switch {
		case !skillNameRe.MatchString(s):
			e.add(field, "%q must be lower-case letters, digits and '-' (the directory name under skills/)", s)
		case slices.Contains(p.Skills, s):
			e.add(field, "%q is listed twice", s)
		default:
			files, msg := checkSkill(fsys, s)
			if msg != "" {
				e.add(field, "%s", msg)
				continue
			}
			for k, v := range files {
				p.skills[k] = v
			}
			p.Skills = append(p.Skills, s)
		}
	}

	if len(f.MCPServers) > maxServers {
		e.add("mcp_servers", "%d servers; at most %d", len(f.MCPServers), maxServers)
	}
	names := make([]string, 0, len(f.MCPServers))
	for n := range f.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := f.MCPServers[n]
		field := "mcp_servers." + n
		switch {
		case !serverRe.MatchString(n) || strings.Contains(n, "__"):
			e.add(field, "the name must be lower-case letters, digits, '-' and '_' (no \"__\"), at most 32 characters")
			continue
		case reservedServers[n]:
			e.add(field, "the name %q is reserved (agent-office's team server or a Claude Code built-in); choose another", n)
			continue
		}
		if msg := checkServer(s.Command, s.Args, s.URL, allow); msg != "" {
			e.add(field, "%s", msg)
			continue
		}
		p.MCPServers[n] = MCPServer{Command: s.Command, Args: s.Args, URL: s.URL}
	}

	if p.Containerfile != "" && len(e.Problems) == 0 {
		if _, err := p.BuildFiles(); err != nil {
			e.add("toolchain", "%v", err)
		}
	}
	if len(e.Problems) > 0 {
		return nil, e
	}
	return p, nil
}

// yamlProblems turns decoder errors into problems, naming unknown fields in
// plain words.
func yamlProblems(e *Error, err error) {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		e.add("", "not valid YAML: %s", strings.TrimPrefix(err.Error(), "yaml: "))
		return
	}
	for _, s := range te.Errors {
		if m := unknownRe.FindStringSubmatch(s); m != nil {
			s = unknownRe.ReplaceAllString(s, "unknown field \""+m[1]+"\" (check the spelling and indentation)")
		}
		e.add("", "%s", s)
	}
}

func oneLine(s string, max int, required bool) string {
	switch {
	case s == "" && required:
		return "missing; say in one line what the pack is for"
	case len([]rune(s)) > max:
		return fmt.Sprintf("longer than %d characters", max)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "must be one line of text (no line breaks or control characters)"
	}
	return ""
}

// relPath checks a path inside the pack directory.
func relPath(p string) string {
	if !fs.ValidPath(p) || p == "." {
		return fmt.Sprintf("%q must be a relative path inside the pack directory (no leading /, no ..)", p)
	}
	return ""
}

func readFile(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s not found in the pack directory", name)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d KiB", name, max>>10)
	}
	return b, nil
}

// checkContainerfile requires `ARG BASE_IMAGE` before the first FROM and
// `FROM ${BASE_IMAGE}` as the last stage, so every pack image is built on the
// base image the sandbox relies on.
func checkContainerfile(src []byte) string {
	var instr [][]string
	var cur strings.Builder
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if t == "" || strings.HasPrefix(t, "#") { // comments are dropped inside continuations too
			continue
		}
		if c, ok := strings.CutSuffix(t, `\`); ok {
			cur.WriteString(c + " ")
			continue
		}
		cur.WriteString(t)
		if f := strings.Fields(cur.String()); len(f) > 0 {
			instr = append(instr, f)
		}
		cur.Reset()
	}
	if f := strings.Fields(cur.String()); len(f) > 0 {
		instr = append(instr, f)
	}
	firstFrom, lastFrom, arg, entrypoint := -1, -1, false, false
	for i, f := range instr {
		switch strings.ToUpper(f[0]) {
		case "FROM":
			if firstFrom < 0 {
				firstFrom = i
			}
			lastFrom, entrypoint = i, false
		case "ENTRYPOINT":
			entrypoint = true
		case "ARG":
			if firstFrom < 0 && len(f) > 1 && strings.SplitN(f[1], "=", 2)[0] == "BASE_IMAGE" {
				arg = true
			}
		}
	}
	if lastFrom < 0 {
		return "the Containerfile has no FROM; start it with `ARG BASE_IMAGE` and `FROM ${BASE_IMAGE}`"
	}
	if !arg {
		return "the Containerfile must declare `ARG BASE_IMAGE` before its first FROM; agent-office passes the base image's tag in it"
	}
	f := instr[lastFrom]
	img := ""
	for _, w := range f[1:] {
		if !strings.HasPrefix(w, "--") {
			img = w
			break
		}
	}
	if img != "${BASE_IMAGE}" && img != "$BASE_IMAGE" {
		return fmt.Sprintf("the last stage must start with `FROM ${BASE_IMAGE}`, not %q: workers rely on the base image's git, certificates and user", strings.Join(f, " "))
	}
	if entrypoint {
		return "the last stage sets an ENTRYPOINT; remove it: agent-office starts workers with its own init and command"
	}
	return ""
}

// checkSkill requires skills/<name>/SKILL.md with a description in its
// frontmatter (code.claude.com/docs/en/skills), bounds the skill's size and
// returns its files, so what agents get is exactly what was checked.
func checkSkill(fsys fs.FS, name string) (map[string][]byte, string) {
	dir := path.Join("skills", name)
	md, err := readFile(fsys, path.Join(dir, "SKILL.md"), maxSkillMDBytes)
	if err != nil {
		return nil, fmt.Sprintf("%v (a skill is a directory skills/%s/ with a SKILL.md)", err, name)
	}
	body := strings.TrimPrefix(string(md), "\ufeff")
	rest, ok := strings.CutPrefix(body, "---\n")
	end := strings.Index(rest, "\n---")
	if !ok || end < 0 {
		return nil, fmt.Sprintf("skills/%s/SKILL.md must start with YAML frontmatter between --- lines, with a description", name)
	}
	var front map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end]), &front); err != nil {
		return nil, fmt.Sprintf("skills/%s/SKILL.md: frontmatter is not valid YAML: %s", name, strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if d, _ := front["description"].(string); strings.TrimSpace(d) == "" {
		return nil, fmt.Sprintf("skills/%s/SKILL.md: the frontmatter needs a description (Claude uses it to decide when to load the skill)", name)
	}
	if n, ok := front["name"]; ok && n != name {
		return nil, fmt.Sprintf("skills/%s/SKILL.md: frontmatter name %v differs from the directory name %q", name, n, name)
	}
	files := map[string][]byte{}
	var size int64
	err = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := readFile(fsys, p, maxSkillBytes)
		if err != nil {
			return err
		}
		if size += int64(len(b)); size > maxSkillBytes || len(files) >= maxFiles {
			return fmt.Errorf("skills/%s is larger than %d KiB or has more than %d files", name, maxSkillBytes>>10, maxFiles)
		}
		files[p] = b
		return nil
	})
	if err != nil {
		return nil, err.Error()
	}
	return files, ""
}

func checkServer(command string, args []string, rawURL string, allow sandbox.Allowlist) string {
	switch {
	case command != "" && rawURL != "":
		return "set either command (a stdio server inside the worker) or url (an HTTPS server), not both"
	case command == "" && rawURL == "":
		return "set command (a stdio server inside the worker) or url (an HTTPS server)"
	case command != "":
		if !strings.HasPrefix(command, "/") || path.Clean(command) != command || strings.ContainsAny(command, "\n\x00") || len(command) > 256 {
			return fmt.Sprintf("command %q must be an absolute, clean path inside the worker image, such as /usr/local/bin/tool", command)
		}
		if len(args) > maxArgs {
			return fmt.Sprintf("%d args; at most %d", len(args), maxArgs)
		}
		for i, a := range args {
			if strings.ContainsAny(a, "\n\x00") || len(a) > 1024 {
				return fmt.Sprintf("args[%d] must be one line of at most 1024 characters", i)
			}
		}
		return ""
	}
	if len(args) > 0 {
		return "args only apply to command servers"
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Sprintf("url %q must be an https:// URL without credentials or fragment", rawURL)
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Sprintf("url %q: the egress proxy only allows port 443", rawURL)
	}
	host := u.Hostname()
	if !sandbox.ValidHost(host) {
		return fmt.Sprintf("url %q must name a lower-case host with at least two labels, such as docs.example.com (no IP address)", rawURL)
	}
	if !allow.Allows(host) {
		return fmt.Sprintf("the host %s is not in egress.allow; add it there, since the server is reached through the egress proxy", host)
	}
	return ""
}
