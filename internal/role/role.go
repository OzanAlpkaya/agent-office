// Package role loads and validates team roles (ADR 0029): declarative YAML
// that configures one kind of agent (its prompt, team tools, denied Claude
// Code tools, model, workspace mode and who it reports to) on top of one of
// the behaviours the engine implements.
//
// A role cannot add authority. Behaviours, their maximum team tools and every
// authorization rule are code; a role chooses a behaviour and may only narrow
// it. Roles come from the binary or from a directory the user owns, never from
// the repository the agents work on.
package role

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Schema is the role format version this binary reads.
const Schema = 1

// File is the role definition inside a role directory.
const File = "role.yaml"

// Limits on role contents.
const (
	maxRoleBytes   = 64 << 10
	maxPromptChars = 12000
	maxDescription = 300
	maxDeny        = 32
	maxModel       = 100
)

// Behaviour is what the engine does with an agent of the role: its authority,
// its lifetime and what its report means (ADR 0017, 0021, 0029–0034).
type Behaviour string

// The behaviours the engine implements.
const (
	Architect Behaviour = "architect" // agrees the plan with the user, read-only
	Lead      Behaviour = "lead"      // executes the plan, assigns sub-tasks (the Tech Lead)
	Worker    Behaviour = "worker"    // builds one sub-task on its own branch
	QA        Behaviour = "qa"        // integrates reported branches and tests them
	Reviewer  Behaviour = "reviewer"  // reviews one pull request
	Assistant Behaviour = "assistant" // briefs the user, curates project memory
)

// Behaviours lists every behaviour in a stable order.
var Behaviours = []Behaviour{Architect, Lead, Worker, QA, Reviewer, Assistant}

// Who a role reports to (reports_to).
const (
	ToUser      = "user"
	ToArchitect = "architect"
	ToLead      = "lead"
	ToNone      = "none"
)

type spec struct {
	tools     []string // the maximum team tool set; the default too
	reportsTo []string // allowed reports_to values; the first is the default
	readOnly  bool     // default workspace mode
}

// behaviours holds what each behaviour may have. This table is the ceiling a
// role can only narrow.
var behaviours = map[Behaviour]spec{
	Architect: {tools: []string{mcp.ToolListTasks, mcp.ToolSendMessage, mcp.ToolMessageUser, mcp.ToolSubmitPlan, mcp.ToolListMemory},
		reportsTo: []string{ToUser}, readOnly: true},
	Lead: {tools: []string{mcp.ToolCreateTask, mcp.ToolAssignTask, mcp.ToolListTasks, mcp.ToolSendMessage,
		mcp.ToolMessageUser, mcp.ToolStartQA, mcp.ToolReportDone, mcp.ToolRequestAccess, mcp.ToolListMemory},
		reportsTo: []string{ToArchitect, ToUser}, readOnly: true},
	Worker: {tools: []string{mcp.ToolListTasks, mcp.ToolSendMessage, mcp.ToolMessageUser, mcp.ToolReportDone,
		mcp.ToolReportNoChange, mcp.ToolRequestAccess, mcp.ToolListMemory}, reportsTo: []string{ToLead}},
	// list_memory: accepted entries only for everyone but the assistant (ADR 0037); reviewers read the
	// most injectable input there is and get no more than their prompt.
	QA: {tools: []string{mcp.ToolListTasks, mcp.ToolSendMessage, mcp.ToolReportQA, mcp.ToolRequestAccess, mcp.ToolListMemory},
		reportsTo: []string{ToLead}},
	Reviewer:  {tools: []string{mcp.ToolListTasks, mcp.ToolSubmitReview}, reportsTo: []string{ToNone}},
	Assistant: {tools: []string{mcp.ToolGetBriefing, mcp.ToolMessageUser, mcp.ToolListMemory, mcp.ToolProposeMemory}, reportsTo: []string{ToUser}, readOnly: true},
}

// MaxTools returns the team tools a behaviour may have at most.
func MaxTools(b Behaviour) []string { return slices.Clone(behaviours[b].tools) }

// Valid reports whether b is a behaviour the engine implements.
func (b Behaviour) Valid() bool { _, ok := behaviours[b]; return ok }

// Role is a validated role definition.
type Role struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Behaviour   Behaviour `json:"behaviour"`
	Model       string    `json:"model,omitempty"` // empty: the daemon's --model
	ReportsTo   string    `json:"reports_to"`
	ReadOnly    bool      `json:"read_only"`
	TeamTools   []string  `json:"team_tools"`
	Deny        []string  `json:"deny"` // Claude Code permission rules denied to the agent
	Prompt      string    `json:"-"`
	Source      string    `json:"source"` // SourceBuiltin or the role directory
}

// SourceBuiltin is Role.Source for roles embedded in the binary.
const SourceBuiltin = "builtin"

// ReadOnlyDeny are the Claude Code tools denied to read-only roles; Bash is
// stopped by the read-only mount (sandbox) or the permission prompt (host).
var ReadOnlyDeny = []string{"Edit", "Write", "NotebookEdit"}

// DenyRules returns the role's deny rules plus, for a read-only role, the file
// editing tools.
func (r *Role) DenyRules() []string {
	out := slices.Clone(r.Deny)
	if r.ReadOnly {
		for _, d := range ReadOnlyDeny {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// HasTool reports whether the role lists team tool name.
func (r *Role) HasTool(name string) bool { return slices.Contains(r.TeamTools, name) }

// Placeholders are replaced in prompts by the engine.
const (
	PlaceholderMaxSubTasks = "{max_sub_tasks}"
	PlaceholderMaxWorkers  = "{max_workers}"
)

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]*$`)
	// A Claude Code permission rule: a tool name, optionally with a specifier
	// in parentheses (code.claude.com/docs/en/permissions).
	denyRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\([^()\n]{1,200}\))?$`)
	unknownRe = regexp.MustCompile(`field (\S+) not found in type \S+`)
)

// Problem is one validation finding: the field and what to do about it.
type Problem struct {
	Field string
	Msg   string
}

// Error lists every problem found in one role.
type Error struct {
	File     string
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	n := len(e.Problems)
	fmt.Fprintf(&b, "%s: invalid role (%d problem%s, see docs/roles.md):", e.File, n, map[bool]string{true: "s"}[n != 1])
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

// file is role.yaml as written; decoding is strict (unknown fields fail).
type file struct {
	Schema      *int   `yaml:"schema"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Behaviour   string `yaml:"behaviour"`
	Model       string `yaml:"model"`
	ReportsTo   string `yaml:"reports_to"`
	Workspace   string `yaml:"workspace"`
	Tools       *struct {
		Team []string `yaml:"team"`
		Deny []string `yaml:"deny"`
	} `yaml:"tools"`
	Prompt string `yaml:"prompt"`
}

// load reads and validates the role in fsys (the role directory). dir is the
// directory's name, which must equal the role's name.
func load(fsys fs.FS, dir, source, label string) (*Role, error) {
	e := &Error{File: label}
	raw, err := readFile(fsys, File, maxRoleBytes)
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
		e.add("", "more than one YAML document (a second ---); a role is a single document")
	}
	r := &Role{Source: source}
	switch {
	case f.Schema == nil:
		e.add("schema", "missing; add `schema: %d` as the first line", Schema)
	case *f.Schema != Schema:
		e.add("schema", "version %d is not supported by this agent-office (it reads schema %d)", *f.Schema, Schema)
	}
	switch r.Name = f.Name; {
	case r.Name == "":
		e.add("name", "missing; add `name: %s` (the directory name)", dir)
	case !nameRe.MatchString(r.Name):
		e.add("name", "%q must be lower-case letters, digits and '-', start with a letter and be at most 32 characters", r.Name)
	case r.Name != dir:
		e.add("name", "%q does not match the role directory %q; rename one of them", r.Name, dir)
	}
	r.Description = strings.TrimSpace(f.Description)
	switch {
	case r.Description == "":
		e.add("description", "missing; say in one line what the role does")
	case len([]rune(r.Description)) > maxDescription:
		e.add("description", "longer than %d characters", maxDescription)
	case strings.IndexFunc(r.Description, unicode.IsControl) >= 0:
		e.add("description", "must be one line of text (no line breaks or control characters)")
	}
	r.Behaviour = Behaviour(f.Behaviour)
	sp, ok := behaviours[r.Behaviour]
	if !ok {
		e.add("behaviour", "%q is not a behaviour; use one of %s", f.Behaviour, joinBehaviours())
	}
	if r.Model = strings.TrimSpace(f.Model); r.Model != "" && (len(r.Model) > maxModel || !modelRe.MatchString(r.Model)) {
		e.add("model", "%q is not a model name; use an alias such as sonnet or a model id such as claude-sonnet-5-5", f.Model)
	}
	if ok {
		switch r.ReportsTo = f.ReportsTo; {
		case r.ReportsTo == "":
			r.ReportsTo = sp.reportsTo[0]
		case !slices.Contains(sp.reportsTo, r.ReportsTo):
			e.add("reports_to", "%q is not allowed for behaviour %s; use %s", r.ReportsTo, r.Behaviour,
				strings.Join(sp.reportsTo, " or "))
		}
		switch f.Workspace {
		case "":
			r.ReadOnly = sp.readOnly
		case "read-only":
			r.ReadOnly = true
		case "read-write":
			r.ReadOnly = false
		default:
			e.add("workspace", "%q must be read-only or read-write", f.Workspace)
		}
		r.TeamTools = slices.Clone(sp.tools)
	}
	if f.Tools != nil {
		if f.Tools.Team != nil && ok {
			r.TeamTools = nil
			for i, t := range f.Tools.Team {
				field := fmt.Sprintf("tools.team[%d]", i)
				switch {
				case !slices.Contains(sp.tools, t):
					e.add(field, "%q is not a team tool of behaviour %s; it may have %s", t, r.Behaviour, strings.Join(sp.tools, ", "))
				case slices.Contains(r.TeamTools, t):
					e.add(field, "%q is listed twice", t)
				default:
					r.TeamTools = append(r.TeamTools, t)
				}
			}
			for _, need := range required(r.Behaviour) {
				if !slices.Contains(r.TeamTools, need) {
					e.add("tools.team", "behaviour %s needs %s (it is how the agent hands its result back); add it", r.Behaviour, need)
				}
			}
		}
		if len(f.Tools.Deny) > maxDeny {
			e.add("tools.deny", "%d rules; at most %d", len(f.Tools.Deny), maxDeny)
		}
		for i, d := range f.Tools.Deny {
			field := fmt.Sprintf("tools.deny[%d]", i)
			switch {
			case !denyRe.MatchString(d):
				e.add(field, "%q is not a Claude Code permission rule; use a tool name such as WebFetch or Bash(curl:*)", d)
			case slices.Contains(r.Deny, d):
				e.add(field, "%q is listed twice", d)
			default:
				r.Deny = append(r.Deny, d)
			}
		}
	}
	r.Prompt = strings.TrimSpace(f.Prompt)
	switch {
	case r.Prompt == "":
		e.add("prompt", "missing; describe the role: what the agent does, how it works with the team, what it must not do")
	case len([]rune(r.Prompt)) > maxPromptChars:
		e.add("prompt", "longer than %d characters", maxPromptChars)
	case strings.IndexFunc(r.Prompt, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\t' }) >= 0:
		e.add("prompt", "contains control characters other than line breaks and tabs")
	}
	if len(e.Problems) > 0 {
		return nil, e
	}
	return r, nil
}

// required are the team tools a behaviour cannot work without.
func required(b Behaviour) []string {
	switch b {
	case Architect:
		return []string{mcp.ToolSubmitPlan}
	case Lead:
		return []string{mcp.ToolCreateTask, mcp.ToolAssignTask, mcp.ToolReportDone}
	case Worker:
		return []string{mcp.ToolReportDone}
	case QA:
		return []string{mcp.ToolReportQA}
	case Reviewer:
		return []string{mcp.ToolSubmitReview}
	case Assistant:
		return []string{mcp.ToolGetBriefing, mcp.ToolMessageUser}
	}
	return nil
}

func joinBehaviours() string {
	s := make([]string, len(Behaviours))
	for i, b := range Behaviours {
		s[i] = string(b)
	}
	return strings.Join(s, ", ")
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

func readFile(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s not found in the role directory", name)
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
