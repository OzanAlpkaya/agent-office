// Package rmcheck decides whether a Bash command that Claude Code holds for
// its "dangerous rm" check may run without asking anyone (Phase 20, ADR
// 0065): every rm and rmdir in it must remove only paths that resolve,
// before the command runs, strictly inside the worker's workspace and not
// in .git.
//
// The command comes from the agent, so this is a security decision. It only
// ever says yes for what it resolved itself: the command is parsed with a
// real shell parser (mvdan.cc/sh, bash syntax), and anything it does not
// model (command substitution, unknown variables, loops, functions,
// subshells, rm run through another command, ...) is a "no", which sends the
// decision to the Tech Lead. Everything other than rm in the command is
// already allowed in a sandboxed agent's bypass mode; what this package
// guards is where rm reaches.
package rmcheck

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// MaxCommand bounds the command text that is parsed at all.
const MaxCommand = 16 << 10

// Result is the decision on one command.
type Result struct {
	Auto    bool     // every rm target resolved inside the workspace, not in .git
	Targets []string // the resolved targets (absolute, in the container), when Auto
	Why     string   // why not, for logs and the Tech Lead; agent text is never quoted in it
}

// Lstat reports whether a path in the container is a symlink. A missing path
// is not an error (os.ErrNotExist); any other error makes the command not
// auto.
type Lstat func(containerPath string) (fs.FileMode, error)

// Check decides on command, run from cwd in a container whose workspace is
// workspace (both absolute, e.g. /workspace). lstat may be nil: then no
// symlink check is made (tests of the parser only).
func Check(command, cwd, workspace string, lstat Lstat) Result {
	no := func(format string, a ...any) Result { return Result{Why: fmt.Sprintf(format, a...)} }
	workspace = path.Clean(workspace)
	switch {
	case len(command) > MaxCommand:
		return no("the command is longer than %d bytes", MaxCommand)
	case !path.IsAbs(workspace) || workspace == "/":
		return no("no workspace")
	case !path.IsAbs(cwd):
		return no("the working directory is not an absolute path")
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return no("the command does not parse as bash")
	}
	c := &checker{ws: workspace, lstat: lstat, vars: map[string]string{}}
	if why := c.unsupported(f); why != "" {
		return no("%s", why)
	}
	cwds := []string{path.Clean(cwd)}
	for _, st := range f.Stmts {
		ok, fail, why := c.stmt(st, cwds)
		if why != "" {
			return no("%s", why)
		}
		cwds = union(ok, fail)
	}
	if c.rms == 0 {
		return no("no rm or rmdir in the command")
	}
	return Result{Auto: true, Targets: c.targets}
}

type checker struct {
	ws      string
	lstat   Lstat
	vars    map[string]string // variables assigned a literal earlier in the command
	unknown map[string]bool   // variables assigned something that is not a literal
	rms     int
	targets []string
}

// unknownDir marks a working directory that cannot be known (cd -, cd ~).
const unknownDir = "?"

// unsupported names the first construct the checker does not model.
func (c *checker) unsupported(f *syntax.File) string {
	why := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.CmdSubst:
			why = "it has a command substitution"
		case *syntax.ProcSubst:
			why = "it has a process substitution"
		case *syntax.ArithmExp, *syntax.ArithmCmd, *syntax.LetClause:
			why = "it has arithmetic"
		case *syntax.FuncDecl:
			why = "it defines a function"
		case *syntax.Subshell, *syntax.Block:
			why = "it has a subshell or a block"
		case *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
			why = "it has a loop or a conditional"
		case *syntax.CoprocClause, *syntax.TimeClause:
			why = "it has coproc or time"
		case *syntax.DeclClause:
			why = "it declares variables (" + n.Variant.Value + ")"
		case *syntax.BraceExp:
			why = "it has a brace expansion"
		case *syntax.ExtGlob:
			why = "it has an extended glob"
		case *syntax.Stmt:
			if n.Background || n.Coprocess || n.Disown {
				why = "it runs something in the background"
			}
		}
		return true
	})
	return why
}

// stmt checks one statement run from cwds and returns where it leaves the
// shell when it succeeds and when it fails.
func (c *checker) stmt(st *syntax.Stmt, cwds []string) (ok, fail []string, why string) {
	switch cmd := st.Cmd.(type) {
	case nil:
		return cwds, cwds, ""
	case *syntax.CallExpr:
		ok, fail, why = c.call(cmd, cwds)
	case *syntax.BinaryCmd:
		switch cmd.Op {
		case syntax.AndStmt:
			xOK, xFail, w := c.stmt(cmd.X, cwds)
			if w != "" {
				return nil, nil, w
			}
			before := c.snapshot()
			yOK, yFail, w := c.stmt(cmd.Y, xOK)
			if w != "" {
				return nil, nil, w
			}
			c.mayHaveRun(before)
			ok, fail = yOK, union(xFail, yFail)
		case syntax.OrStmt:
			xOK, xFail, w := c.stmt(cmd.X, cwds)
			if w != "" {
				return nil, nil, w
			}
			before := c.snapshot()
			yOK, yFail, w := c.stmt(cmd.Y, xFail)
			if w != "" {
				return nil, nil, w
			}
			c.mayHaveRun(before)
			ok, fail = union(xOK, yOK), yFail
		case syntax.Pipe, syntax.PipeAll:
			// Each side runs in a subshell: a cd there changes nothing here,
			// and an assignment there is not seen here.
			saved := c.snapshot()
			for _, side := range []*syntax.Stmt{cmd.X, cmd.Y} {
				if _, _, w := c.stmt(side, cwds); w != "" {
					return nil, nil, w
				}
			}
			c.restore(saved)
			ok, fail = cwds, cwds
		default:
			return nil, nil, "it has an operator the check does not model"
		}
	case *syntax.TestClause:
		ok, fail = cwds, cwds // [[ ... ]]: substitutions in it are refused above
	default:
		return nil, nil, fmt.Sprintf("it has a %T", cmd)
	}
	if why != "" {
		return nil, nil, why
	}
	if st.Negated {
		return union(ok, fail), union(ok, fail), ""
	}
	return ok, fail, ""
}

// MentionsRm reports whether a command names rm, rmdir or unlink anywhere:
// Claude Code's dangerous rm check is the only Bash prompt left in bypass
// mode, and only such prompts go to the Tech Lead.
func MentionsRm(command string) bool { return mentionsRm.MatchString(command) }

// mentionsRm finds rm in another command's arguments (wrappers can run a
// command named there).
var mentionsRm = regexp.MustCompile(`(^|[^A-Za-z0-9_-])(rm|rmdir|unlink)([^A-Za-z0-9_-]|$)`)

func (c *checker) call(ce *syntax.CallExpr, cwds []string) (ok, fail []string, why string) {
	if len(ce.Args) == 0 {
		// Assignments only: they set variables for the rest of the command.
		for _, a := range ce.Assigns {
			if a.Name == nil {
				return nil, nil, "it has an assignment the check does not model"
			}
			if specialVar(a.Name.Value) {
				return nil, nil, "it sets a variable that changes how the shell or rm works"
			}
			c.unset(a.Name.Value)
			if a.Append || a.Array != nil || a.Index != nil || a.Naked {
				c.markUnknown(a.Name.Value)
				continue
			}
			if a.Value == nil {
				c.vars[a.Name.Value] = ""
				continue
			}
			if v, ok := c.word(a.Value, cwds); ok && len(v) == 1 && !v[0].glob {
				c.vars[a.Name.Value] = v[0].text
			} else {
				c.markUnknown(a.Name.Value)
			}
		}
		return cwds, cwds, ""
	}
	names, okName := c.word(ce.Args[0], cwds)
	if !okName || len(names) != 1 || (names[0].glob && names[0].text != "[") || hasParam(ce.Args[0]) {
		return nil, nil, "a command name is not a plain word"
	}
	name := path.Base(names[0].text)
	if len(ce.Assigns) > 0 {
		return nil, nil, "it sets variables for a command" // POSIXLY_CORRECT=1 rm ..., CDPATH=/ cd ...
	}
	switch name {
	case "rm", "rmdir":
		if why := c.rm(ce.Args[1:], cwds); why != "" {
			return nil, nil, why
		}
		return cwds, cwds, ""
	case "cd":
		return c.cd(ce.Args[1:], cwds)
	case "[", "test", "true", "false", ":", "echo", "ls", "pwd":
		// Side-effect free: they create, move or link nothing and set no
		// variable (review finding: ln -s .git x; rm -rf x/objects, or
		// printf -v x /etc; rm -r "$x", changed what the check reasoned about).
		for _, w := range ce.Args[1:] {
			if mentionsRm.MatchString(rawText(w)) {
				return nil, nil, "rm is named in another command's arguments"
			}
		}
		return cwds, cwds, ""
	}
	// Anything else may create, move or link the paths rm then removes, or run
	// rm itself: the Tech Lead decides.
	return nil, nil, "it runs another command besides rm, cd and tests"
}

func (c *checker) cd(args []*syntax.Word, cwds []string) (ok, fail []string, why string) {
	var dir string
	switch len(args) {
	case 0:
		return []string{unknownDir}, cwds, "" // cd: home
	case 1:
		v, okW := c.word(args[0], cwds)
		if !okW || len(v) != 1 || v[0].glob || v[0].text == "-" || strings.HasPrefix(v[0].text, "-") || v[0].text == "" {
			return []string{unknownDir}, cwds, ""
		}
		dir = v[0].text
		if hasDotDot(dir) {
			return []string{unknownDir}, cwds, ""
		}
	default:
		return []string{unknownDir}, cwds, ""
	}
	for _, d := range cwds {
		if path.IsAbs(dir) {
			ok = append(ok, path.Clean(dir))
		} else if d == unknownDir {
			ok = append(ok, unknownDir)
		} else {
			ok = append(ok, path.Join(d, dir))
		}
	}
	return dedupe(ok), cwds, ""
}

// rm checks rm's or rmdir's operands.
func (c *checker) rm(args []*syntax.Word, cwds []string) string {
	c.rms++
	operands := 0
	dashdash := false
	for _, a := range args {
		vs, ok := c.word(a, cwds)
		if !ok {
			return "an rm target is not known before the command runs"
		}
		for _, v := range vs {
			if !dashdash && strings.HasPrefix(v.text, "-") && len(v.text) > 1 {
				if v.text == "--" {
					dashdash = true
				}
				continue // an option: rm does not see the quotes
			}
			operands++
			for _, d := range cwds {
				if why := c.target(v, d); why != "" {
					return why
				}
			}
		}
	}
	if operands == 0 {
		return "rm without a target"
	}
	return ""
}

// target checks one operand run from directory d.
func (c *checker) target(v value, d string) string {
	if v.text == "" {
		return "an rm target is empty"
	}
	if hasDotDot(v.text) {
		// The kernel resolves .. after symlinks, not as text (review finding).
		return "an rm target has a .. in it"
	}
	p := v.text
	if !path.IsAbs(p) {
		if d == unknownDir {
			return "an rm target is relative to a directory not known before the command runs"
		}
		p = d + "/" + p
	}
	dir, base := path.Split(p)
	if v.glob && strings.ContainsAny(dir, "*?[") {
		return "an rm target has a wildcard before its last part"
	}
	clean := path.Clean(p)
	trailing := strings.HasSuffix(v.text, "/") || strings.HasSuffix(v.text, "/.")
	if v.glob {
		// A wildcard in the last part: bash does not match a leading dot unless
		// the pattern starts with one, so only a pattern starting with "." (or a
		// bracket, which this does not try to read) can reach .git.
		if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "[") || base == "" {
			return "an rm wildcard can match .git or hidden files"
		}
		parent := path.Clean(dir)
		if parent != c.ws && !inside(parent, c.ws) {
			return "an rm target is outside the workspace"
		}
		if hasGit(parent, c.ws) {
			return "an rm target is in .git"
		}
		if why := c.noSymlinks(parent, true); why != "" {
			return why
		}
		c.targets = append(c.targets, parent+"/"+base)
		return ""
	}
	if !inside(clean, c.ws) {
		if clean == c.ws {
			return "an rm target is the workspace itself"
		}
		return "an rm target is outside the workspace"
	}
	if hasGit(clean, c.ws) {
		return "an rm target is in .git"
	}
	if why := c.noSymlinks(path.Dir(clean), true); why != "" {
		return why
	}
	if trailing {
		// rm -r link/ follows the link.
		if why := c.noSymlinks(clean, false); why != "" {
			return why
		}
	}
	c.targets = append(c.targets, clean)
	return ""
}

// noSymlinks checks that no directory from the workspace down to dir (and
// dir itself when withParents is false, only it) is a symlink: a symlinked
// directory would take rm outside the workspace.
func (c *checker) noSymlinks(dir string, withParents bool) string {
	if c.lstat == nil {
		return ""
	}
	var check []string
	if withParents {
		rel := strings.TrimPrefix(dir, c.ws)
		cur := c.ws
		for _, part := range strings.Split(strings.Trim(rel, "/"), "/") {
			if part == "" {
				continue
			}
			cur = cur + "/" + part
			check = append(check, cur)
		}
	} else {
		check = []string{dir}
	}
	for _, p := range check {
		mode, err := c.lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return "" // nothing below a missing directory can be reached
		}
		if err != nil {
			return "an rm target's directory could not be checked"
		}
		if mode&fs.ModeSymlink != 0 {
			return "an rm target goes through a symlink"
		}
	}
	return ""
}

func hasDotDot(p string) bool { return slices.Contains(strings.Split(p, "/"), "..") }

func inside(p, ws string) bool { return strings.HasPrefix(p, ws+"/") }

func hasGit(p, ws string) bool {
	return slices.Contains(strings.Split(strings.TrimPrefix(p, ws+"/"), "/"), ".git")
}

// value is one word after expansion.
type value struct {
	text   string
	glob   bool // an unquoted *, ? or [ in it
	quoted bool // some of it was quoted (an option is never quoted)
}

var safeUnquoted = regexp.MustCompile(`^[A-Za-z0-9._/@+,:=%-]*$`)

// word expands w the way the check models it: literals, quotes, and $NAME
// or ${NAME} of a variable known here (or PWD). ok is false for anything
// else. Unquoted variables must not split or glob.
func (c *checker) word(w *syntax.Word, cwds []string) ([]value, bool) {
	var b strings.Builder
	v := value{}
	var expand func(parts []syntax.WordPart, quoted bool) bool
	expand = func(parts []syntax.WordPart, quoted bool) bool {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				if !quoted && strings.ContainsAny(p.Value, "{}") {
					return false // a brace expansion ({a,b}, {1..3}) the check does not expand
				}
				s, glob := unescape(p.Value, quoted)
				if strings.HasPrefix(s, "~") && b.Len() == 0 && !quoted {
					return false // the home directory
				}
				b.WriteString(s)
				v.glob = v.glob || glob
			case *syntax.SglQuoted:
				if p.Dollar {
					return false // $'...' escapes
				}
				b.WriteString(p.Value)
				v.quoted = true
			case *syntax.DblQuoted:
				if p.Dollar {
					return false
				}
				v.quoted = true
				if !expand(p.Parts, true) {
					return false
				}
			case *syntax.ParamExp:
				val, ok := c.param(p, cwds)
				if !ok {
					return false
				}
				if !quoted && (!safeUnquoted.MatchString(val) || val == "") {
					return false // it would split, glob or vanish
				}
				b.WriteString(val)
			default:
				return false
			}
		}
		return true
	}
	if !expand(w.Parts, false) {
		return nil, false
	}
	v.text = b.String()
	return []value{v}, true
}

// param is a simple $NAME or ${NAME}.
func (c *checker) param(p *syntax.ParamExp, cwds []string) (string, bool) {
	simple := p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet && p.NestedParam == nil &&
		p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil &&
		p.Split == 0 && p.GlobSubst == 0 && p.RcExpand == 0
	if !simple {
		return "", false
	}
	name := p.Param.Value
	if name == "PWD" && !c.unknown[name] {
		if _, set := c.vars[name]; !set {
			if len(cwds) != 1 || cwds[0] == unknownDir {
				return "", false
			}
			return cwds[0], true
		}
	}
	if c.unknown[name] {
		return "", false
	}
	val, ok := c.vars[name]
	return val, ok
}

// unescape removes backslash escapes from a literal and reports whether an
// unescaped glob character remains (outside double quotes).
func unescape(s string, quoted bool) (string, bool) {
	var b strings.Builder
	glob := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			next := s[i+1]
			if !quoted || strings.IndexByte("$`\"\\\n", next) >= 0 {
				b.WriteByte(next)
				i++
				continue
			}
		}
		if !quoted && (ch == '*' || ch == '?' || ch == '[') {
			glob = true
		}
		b.WriteByte(ch)
	}
	return b.String(), glob
}

// specialVar reports whether assigning name changes how bash expands words
// or finds directories, or what rm does through its environment (IFS,
// CDPATH, GLOBIGNORE, POSIXLY_CORRECT, PATH, ...). Only other names are
// modelled.
func specialVar(name string) bool {
	switch name {
	case "IFS", "CDPATH", "GLOBIGNORE", "GLOBSORT", "FIGNORE", "POSIXLY_CORRECT", "PATH", "HOME", "PWD", "OLDPWD",
		"SHELLOPTS", "BASHOPTS", "ENV", "BASH_ENV", "TMPDIR", "LANG", "LANGUAGE", "PS4", "PROMPT_COMMAND", "TIMEFORMAT":
		return true
	}
	return strings.HasPrefix(name, "BASH") || strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "GLOB")
}

// hasParam reports whether a word has a parameter expansion.
func hasParam(w *syntax.Word) bool {
	found := false
	syntax.Walk(w, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.ParamExp); ok {
			found = true
		}
		return !found
	})
	return found
}

// rawText is a word's source text, for spotting rm in other commands' arguments.
func rawText(w *syntax.Word) string {
	var b strings.Builder
	syntax.Walk(w, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Lit:
			b.WriteString(n.Value)
			b.WriteByte(' ')
		case *syntax.SglQuoted:
			b.WriteString(n.Value)
			b.WriteByte(' ')
		}
		return true
	})
	return b.String()
}

func (c *checker) unset(name string) {
	delete(c.vars, name)
	delete(c.unknown, name)
}

func (c *checker) markUnknown(name string) {
	if c.unknown == nil {
		c.unknown = map[string]bool{}
	}
	delete(c.vars, name)
	c.unknown[name] = true
}

type snapshot struct {
	vars    map[string]string
	unknown map[string]bool
}

func (c *checker) snapshot() snapshot {
	s := snapshot{vars: map[string]string{}, unknown: map[string]bool{}}
	for k, v := range c.vars {
		s.vars[k] = v
	}
	for k, v := range c.unknown {
		s.unknown[k] = v
	}
	return s
}

func (c *checker) restore(s snapshot) { c.vars, c.unknown = s.vars, s.unknown }

// mayHaveRun follows a part of the command that runs only on a condition
// (the right side of && or ||): a variable it changed may or may not hold
// its new value afterwards, so it is unknown (review finding:
// x=/etc/foo; true || x=/workspace/a; rm -rf $x).
func (c *checker) mayHaveRun(before snapshot) {
	changed := map[string]bool{}
	for k, v := range c.vars {
		if old, ok := before.vars[k]; !ok || old != v || before.unknown[k] {
			changed[k] = true
		}
	}
	for k := range before.vars {
		if _, ok := c.vars[k]; !ok {
			changed[k] = true
		}
	}
	for k := range c.unknown {
		changed[k] = true
	}
	c.restore(before)
	for k := range changed {
		c.markUnknown(k)
	}
}

func union(a, b []string) []string { return dedupe(append(append([]string{}, a...), b...)) }

func dedupe(xs []string) []string {
	var out []string
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
