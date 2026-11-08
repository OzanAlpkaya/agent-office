package engine

import (
	"context"
	"strconv"
	"strings"

	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Roles as data (ADR 0029). A role picks one of the behaviours (kinds) the
// engine implements and configures it: prompt, model, team tools (only fewer),
// denied Claude Code tools, read-only workspace. Authority stays with the
// kind.

// resolveRole finds the role an agent starts with and checks that the request
// fits its behaviour.
func (e *Engine) resolveRole(req StartRequest) (*role.Role, error) {
	name := req.Role
	switch name {
	case "":
		name = RoleWorker
	case roleLeadAlias:
		name = RoleTechLead
	}
	def, ok := e.opts.Roles.Get(name)
	if !ok {
		var names []string
		for _, r := range e.opts.Roles.List() {
			names = append(names, r.Name)
		}
		return nil, invalid("unknown role %q; roles: %s", name, strings.Join(names, ", "))
	}
	switch k := string(def.Behaviour); {
	case k == KindReviewer && req.reviewID == 0:
		return nil, invalid("reviewers are started by agent-office for a pull request")
	case k == KindQA && !req.qa:
		return nil, invalid("QA is started by the Tech Lead with start_qa")
	case (k == KindLead || k == KindArchitect) && req.TaskID == "":
		return nil, invalid("a %s needs a goal: start it on a top-level task", def.Name)
	case k == KindAssistant && req.TaskID != "":
		return nil, invalid("the assistant works across goals; start it without a task")
	}
	return def, nil
}

// roleOf returns the definition of an agent's role, or the built-in role of
// its kind if the role is gone (a user role removed since the agent ran).
func (e *Engine) roleOf(name, kind string) *role.Role {
	if def, ok := e.opts.Roles.Get(name); ok && string(def.Behaviour) == kind {
		return def
	}
	return e.opts.Roles.Default(role.Behaviour(kind))
}

// modelOf is the model an agent of def runs with: the role's, else --model.
func (e *Engine) modelOf(def *role.Role) string {
	if def.Model != "" {
		return def.Model
	}
	return e.opts.Model
}

// single reports whether at most one agent of kind runs at a time (ADR 0017:
// one goal at a time, so one lead, architect and QA; one assistant).
func single(kind string) bool {
	return kind == KindLead || kind == KindArchitect || kind == KindQA || kind == KindAssistant
}

// movesTask reports whether starting an agent of kind moves its task to
// in_progress (ADR 0011). Reviewers and QA work on a task without changing it.
func movesTask(kind string) bool {
	return kind == KindWorker || kind == KindLead || kind == KindArchitect
}

// imports reports whether a sandboxed agent of kind has work to import into
// the main repository on exit (ADR 0014): its commits are its result.
func imports(kind string) bool { return kind == KindWorker || kind == KindQA }

// expandPrompt fills a role prompt's placeholders.
func (e *Engine) expandPrompt(def *role.Role) string {
	return strings.NewReplacer(
		role.PlaceholderMaxSubTasks, strconv.Itoa(MaxSubTasks),
		role.PlaceholderMaxWorkers, strconv.Itoa(max(e.opts.MaxAgents-1, 1)),
	).Replace(def.Prompt)
}

// initialPrompt is the first prompt of a new agent: its role's prompt, then
// what the engine adds for its behaviour (the task, the protocol, the
// framing), so a role pack cannot change how work is reported.
func (e *Engine) initialPrompt(ctx context.Context, def *role.Role, task *db.Task, branch string) string {
	var b strings.Builder
	b.WriteString(e.expandPrompt(def))
	switch string(def.Behaviour) {
	case KindLead:
		if task != nil {
			b.WriteString("\n\n" + goalSection(task))
			if p := e.planSection(ctx, task); p != "" {
				b.WriteString("\n" + p)
			}
		}
	case KindArchitect:
		if task != nil {
			b.WriteString("\n\n" + goalSection(task))
		}
	case KindWorker:
		if task == nil {
			return "" // a user's own agent without a task starts at the prompt
		}
		b.WriteString("\n\n" + taskPrompt(task.Title, task.Description, branch) + e.workerRules(task))
	}
	return b.String()
}

// goalSection is the goal as leads and architects see it.
func goalSection(task *db.Task) string {
	s := "Your goal:\n\nGoal: " + task.Title + "\n"
	if task.Description != "" {
		s += "\n" + task.Description + "\n"
	}
	return s
}

// RoleInfo is a role as the UI shows it; the prompt stays in the role file.
type RoleInfo struct {
	*role.Role
	Model string `json:"model"` // the model it runs with: its own, else --model ("" = Claude Code's default)
}

// Roles lists the team roles of this daemon run (ADR 0029).
func (e *Engine) Roles() []RoleInfo {
	var out []RoleInfo
	for _, r := range e.opts.Roles.List() {
		out = append(out, RoleInfo{Role: r, Model: e.modelOf(r)})
	}
	return out
}

// qaGate refuses a goal's report until QA passed on the current work (ADR
// 0033); filled in with QA. Without --qa it always passes.
func (e *Engine) qaGate(ctx context.Context, goal db.Task, children []db.Task) error {
	return nil
}
