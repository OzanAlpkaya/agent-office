package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/role"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Architect plans and specs (ADR 0031). The architect submits a structured
// plan; it is stored as a version, shown, edited and approved by the user.
// Approval is deterministic: the spec is committed as docs/specs/<goal>.md on
// the goal's spec branch, the sub-tasks are created from it and a Tech Lead
// starts.

// Spec statuses (specs.status).
const (
	SpecProposed   = "proposed"
	SpecApproved   = "approved"
	SpecSuperseded = "superseded"
)

// Plan limits.
const (
	maxPlanSummary    = 2000
	maxPlanDesign     = 20000
	maxPlanRisks      = 4000
	maxPlanFiles      = 50
	maxPlanAcceptance = 20
	maxCriterionLen   = 500
)

// Plan is an architect's plan, as submitted and as stored (JSON).
type Plan = mcp.SubmitPlanInput

// Spec is one version of a goal's plan. Its text was written by an agent or
// the user: render it as text only.
type Spec struct {
	ID         int64      `json:"id"`
	GoalID     string     `json:"goal_id"`
	Version    int64      `json:"version"`
	Status     string     `json:"status"`
	Author     string     `json:"author,omitempty"` // the architect's agent id; empty: the user
	Plan       Plan       `json:"plan"`
	Markdown   string     `json:"markdown"`
	Branch     string     `json:"branch,omitempty"` // once approved: the spec branch
	Commit     string     `json:"commit,omitempty"`
	Path       string     `json:"path,omitempty"` // docs/specs/<goal>.md
	CreatedAt  time.Time  `json:"created_at"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
}

func specFromRow(r db.Spec) Spec {
	s := Spec{ID: r.ID, GoalID: r.GoalID, Version: r.Version, Status: r.Status, Author: r.Author, Markdown: r.Markdown,
		Branch: r.Branch, Commit: r.CommitSha, Path: r.Path, CreatedAt: r.CreatedAt.UTC()}
	_ = json.Unmarshal([]byte(r.Plan), &s.Plan)
	if r.ApprovedAt.Valid {
		t := r.ApprovedAt.Time.UTC()
		s.ApprovedAt = &t
	}
	return s
}

var specKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// validFilePattern checks a sub-task's file entry: a repository-relative path,
// a directory ending in '/', or a glob (ADR 0032).
func validFilePattern(p string) bool {
	if p == "" || len(p) > 300 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\n\r\t") {
		return false
	}
	q := strings.TrimSuffix(p, "/")
	if q == "" || path.Clean(q) != q || q == "." || q == ".." || strings.HasPrefix(q, "../") {
		return false
	}
	_, err := path.Match(q, "x") // a malformed glob
	return err == nil
}

// normalizeList trims, drops empties and checks a list of short texts.
func normalizeList(in []string, maxItems, maxLen int, field string, check func(string) bool) ([]string, error) {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if utf8.RuneCountInString(s) > maxLen || (check != nil && !check(s)) {
			return nil, invalid("%s: %q is not valid", field, truncateRunes(s, 80))
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > maxItems {
		return nil, invalid("%s: at most %d items", field, maxItems)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// workerRole checks that name is a role with the worker behaviour ("" is the
// built-in worker) and returns its name.
func (e *Engine) workerRole(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	r, ok := e.opts.Roles.Get(name)
	if !ok || r.Behaviour != role.Worker {
		return "", invalid("role %q is not a worker role; use one of %s", name, strings.Join(e.workerRoles(), ", "))
	}
	return name, nil
}

// workerRoles lists the roles a sub-task can be built by.
func (e *Engine) workerRoles() []string {
	var out []string
	for _, r := range e.opts.Roles.List() {
		if r.Behaviour == role.Worker {
			out = append(out, r.Name)
		}
	}
	return out
}

// validatePlan normalizes p in place and checks it (ADR 0031).
func (e *Engine) validatePlan(p *Plan) error {
	var err error
	if p.Summary, err = normalizeText(p.Summary, maxPlanSummary, "summary"); err != nil {
		return err
	}
	if p.Design = strings.TrimSpace(p.Design); utf8.RuneCountInString(p.Design) > maxPlanDesign || !utf8.ValidString(p.Design) {
		return invalid("design: at most %d characters of text", maxPlanDesign)
	}
	if p.Risks = strings.TrimSpace(p.Risks); utf8.RuneCountInString(p.Risks) > maxPlanRisks || !utf8.ValidString(p.Risks) {
		return invalid("risks: at most %d characters of text", maxPlanRisks)
	}
	if n := len(p.SubTasks); n == 0 || n > MaxSubTasks {
		return invalid("sub_tasks: 1-%d sub-tasks", MaxSubTasks)
	}
	keys := map[string]bool{}
	for i := range p.SubTasks {
		st := &p.SubTasks[i]
		field := fmt.Sprintf("sub_tasks[%d]", i)
		st.Key = strings.TrimSpace(st.Key)
		if !specKeyRe.MatchString(st.Key) {
			return invalid("%s.key %q: lower-case letters, digits and -, at most 32", field, st.Key)
		}
		if keys[st.Key] {
			return invalid("%s.key %q is used twice", field, st.Key)
		}
		keys[st.Key] = true
		if st.Title, err = normalizeTitle(st.Title); err != nil {
			return fmt.Errorf("%s.title: %w", field, err)
		}
		if st.Description, err = normalizeDescription(st.Description); err != nil {
			return fmt.Errorf("%s.description: %w", field, err)
		}
		if st.Role, err = e.workerRole(strings.TrimSpace(st.Role)); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if st.Files, err = normalizeList(st.Files, maxPlanFiles, 300, field+".files", validFilePattern); err != nil {
			return err
		}
		if st.Acceptance, err = normalizeList(st.Acceptance, maxPlanAcceptance, maxCriterionLen, field+".acceptance", nil); err != nil {
			return err
		}
		if st.DependsOn, err = normalizeList(st.DependsOn, MaxSubTasks, 32, field+".depends_on", nil); err != nil {
			return err
		}
	}
	deps := map[string][]string{}
	for _, st := range p.SubTasks {
		for _, d := range st.DependsOn {
			if !keys[d] {
				return invalid("sub-task %s depends on %q, which is not a key of the plan", st.Key, d)
			}
			if d == st.Key {
				return invalid("sub-task %s depends on itself", st.Key)
			}
		}
		deps[st.Key] = st.DependsOn
	}
	if cycle := findCycle(deps); cycle != nil {
		return invalid("the dependencies form a cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// findCycle returns a dependency cycle, or nil.
func findCycle(deps map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(string) []string
	visit = func(k string) []string {
		color[k] = grey
		stack = append(stack, k)
		for _, d := range deps[k] {
			switch color[d] {
			case grey:
				i := slices.Index(stack, d)
				return append(slices.Clone(stack[i:]), d)
			case white:
				if c := visit(d); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[k] = black
		return nil
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if color[k] == white {
			if c := visit(k); c != nil {
				return c
			}
		}
	}
	return nil
}

// renderSpec is the Markdown of a plan, as committed to docs/specs.
func renderSpec(goal db.Task, p Plan, version int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", goal.Title)
	fmt.Fprintf(&b, "_Plan version %d for goal `%s`, agreed in agent-office and approved by the user. "+
		"Sub-tasks are executed by the Tech Lead in dependency order._\n\n", version, goal.ID)
	b.WriteString("## Summary\n\n" + p.Summary + "\n\n")
	if p.Design != "" {
		b.WriteString("## Design\n\n" + p.Design + "\n\n")
	}
	b.WriteString("## Sub-tasks\n\n")
	for _, st := range p.SubTasks {
		fmt.Fprintf(&b, "### `%s`: %s\n\n", st.Key, st.Title)
		if st.Description != "" {
			b.WriteString(st.Description + "\n\n")
		}
		if st.Role != "" {
			fmt.Fprintf(&b, "- **Role:** %s\n", st.Role)
		}
		if len(st.DependsOn) > 0 {
			fmt.Fprintf(&b, "- **Depends on:** %s\n", "`"+strings.Join(st.DependsOn, "`, `")+"`")
		}
		if len(st.Files) > 0 {
			fmt.Fprintf(&b, "- **Files:** %s\n", "`"+strings.Join(st.Files, "`, `")+"`")
		}
		if len(st.Acceptance) > 0 {
			b.WriteString("- **Acceptance criteria:**\n")
			for _, a := range st.Acceptance {
				b.WriteString("  - " + a + "\n")
			}
		}
		b.WriteString("\n")
	}
	if p.Risks != "" {
		b.WriteString("## Risks\n\n" + p.Risks + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func (t team) SubmitPlan(ctx context.Context, caller string, in mcp.SubmitPlanInput) (mcp.SubmitPlanOutput, error) {
	s, err := t.e.submitPlan(ctx, caller, in)
	if err != nil {
		return mcp.SubmitPlanOutput{}, userErr(err)
	}
	return mcp.SubmitPlanOutput{SpecID: s.ID, Version: s.Version, Status: s.Status,
		Next: "Submitted. End your turn: the user reviews the plan in agent-office; their questions or change " +
			"requests are delivered to you as new input. Approval ends your session."}, nil
}

func (e *Engine) submitPlan(ctx context.Context, caller string, p Plan) (Spec, error) {
	e.mu.Lock()
	a, err := e.callerLocked(caller)
	var info AgentInfo
	if err == nil {
		info = a.info
		if info.Kind != KindArchitect {
			err = forbidden("only the architect submits a plan")
		}
	}
	e.mu.Unlock()
	if err != nil {
		return Spec{}, err
	}
	goal, err := e.getTask(ctx, info.TaskID)
	if err != nil {
		return Spec{}, fmt.Errorf("your goal: %w", err)
	}
	if goal.Status == StatusDone {
		return Spec{}, invalid("the goal is done")
	}
	if err := e.validatePlan(&p); err != nil {
		return Spec{}, err
	}
	s, err := e.saveSpec(ctx, goal, caller, p)
	if err != nil {
		return Spec{}, err
	}
	if _, err := e.addInbox(ctx, "", goal.ID, goal.ID, "", InboxPlan, fmt.Sprintf(
		"The architect's plan (version %d) for %q is ready for your approval: %d sub-task(s). Open the goal on the board "+
			"to read, edit or approve it.", s.Version, goal.Title, len(p.SubTasks))); err != nil {
		e.log.Warn("plan notice", "err", err)
	}
	e.log.Info("plan submitted", "agent_id", caller, "task_id", goal.ID, "spec_id", s.ID, "version", s.Version,
		"sub_tasks", len(p.SubTasks))
	return s, nil
}

// saveSpec stores a new proposed version of goal's plan; earlier proposals
// are superseded. author is an agent id, or "" for the user.
func (e *Engine) saveSpec(ctx context.Context, goal db.Task, author string, p Plan) (Spec, error) {
	plan, err := json.Marshal(p)
	if err != nil {
		return Spec{}, err
	}
	var row db.Spec
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		v, err := q.NextSpecVersion(ctx, goal.ID)
		if err != nil {
			return err
		}
		if row, err = q.InsertSpec(ctx, db.InsertSpecParams{GoalID: goal.ID, Version: v, Author: author,
			Plan: string(plan), Markdown: renderSpec(goal, p, v), CreatedAt: now()}); err != nil {
			return err
		}
		return q.SupersedeProposedSpecs(ctx, db.SupersedeProposedSpecsParams{GoalID: goal.ID, ID: row.ID})
	})
	if err != nil {
		return Spec{}, err
	}
	s := specFromRow(row)
	e.publishSpecs(ctx, goal.ID)
	return s, nil
}

// publishSpecs sends every version of a goal's plan to the UI.
func (e *Engine) publishSpecs(ctx context.Context, goalID string) {
	rows, err := e.q.ListGoalSpecs(ctx, goalID)
	if err != nil {
		return
	}
	for _, r := range rows {
		s := specFromRow(r)
		e.publish(Update{Type: "spec", Spec: &s})
	}
}

// Specs lists every version of every goal's plan, newest first per goal.
func (e *Engine) Specs(ctx context.Context) ([]Spec, error) {
	rows, err := e.q.ListProjectSpecs(ctx, e.project.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Spec, 0, len(rows))
	for _, r := range rows {
		out = append(out, specFromRow(r))
	}
	return out, nil
}

func (e *Engine) getSpec(ctx context.Context, id int64) (db.Spec, db.Task, error) {
	r, err := e.q.GetSpec(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Spec{}, db.Task{}, ErrNotFound
	}
	if err != nil {
		return db.Spec{}, db.Task{}, err
	}
	goal, err := e.getTask(ctx, r.GoalID)
	if err != nil {
		return db.Spec{}, db.Task{}, err
	}
	return r, goal, nil
}

// EditSpec saves the user's edit of a plan as a new proposed version.
func (e *Engine) EditSpec(ctx context.Context, id int64, p Plan) (Spec, error) {
	_, goal, err := e.getSpec(ctx, id)
	if err != nil {
		return Spec{}, err
	}
	if goal.Status == StatusDone {
		return Spec{}, invalid("the goal is done")
	}
	if err := e.validatePlan(&p); err != nil {
		return Spec{}, err
	}
	return e.saveSpec(ctx, goal, "", p)
}

// ApproveResult is what approving a plan did.
type ApproveResult struct {
	Spec      Spec       `json:"spec"`
	Created   []string   `json:"created"` // sub-task ids
	Updated   []string   `json:"updated"`
	Deleted   []string   `json:"deleted"`
	Kept      []string   `json:"kept"` // sub-tasks already worked on, left as they are
	Lead      *AgentInfo `json:"lead,omitempty"`
	LeadError string     `json:"lead_error,omitempty"` // the Tech Lead could not start (e.g. no free slot)
}

// specLocation is where a goal's spec is committed.
func specLocation(goal db.Task) (file, branch string) {
	slug := git.Slug(goal.Title, 40)
	if slug == "" {
		slug = "goal"
	}
	return "docs/specs/" + slug + ".md", git.BranchPrefix + "spec-" + slug + "-" + goal.ID[:min(6, len(goal.ID))]
}

// ApproveSpec is the user's approval of a proposed plan (ADR 0031).
func (e *Engine) ApproveSpec(ctx context.Context, id int64) (ApproveResult, error) {
	row, goal, err := e.getSpec(ctx, id)
	if err != nil {
		return ApproveResult{}, err
	}
	switch {
	case row.Status != SpecProposed:
		return ApproveResult{}, invalid("version %d is %s; only a proposed plan can be approved", row.Version, row.Status)
	case goal.Status == StatusDone:
		return ApproveResult{}, invalid("the goal is done")
	}
	s := specFromRow(row)
	if err := e.validatePlan(&s.Plan); err != nil { // the plan as it is now (roles may have changed)
		return ApproveResult{}, err
	}

	// 1. The spec file on the goal's spec branch: on top of the previous
	// approved version, else the main repository's HEAD.
	repo := e.project.RepoPath
	file, branch := specLocation(goal)
	base, err := e.git.HeadCommit(ctx, repo)
	if err != nil {
		return ApproveResult{}, err
	}
	old := ""
	if exists, err := e.git.BranchExists(ctx, repo, branch); err != nil {
		return ApproveResult{}, err
	} else if exists {
		if old, err = e.git.RevParse(ctx, repo, "refs/heads/"+branch); err != nil {
			return ApproveResult{}, err
		}
		base = old
	}
	msg := fmt.Sprintf("docs: plan for %s (version %d)\n\nApproved by the user in agent-office.\n\nAgent-Office-Goal: %s\n",
		goal.Title, s.Version, goal.ID)
	commit, err := e.git.CommitFile(ctx, repo, base, file, []byte(s.Markdown), msg, e.opts.RuntimeDir)
	if err != nil {
		return ApproveResult{}, fmt.Errorf("commit the spec: %w", err)
	}
	if err := e.git.SetBranch(ctx, repo, branch, commit, old); err != nil {
		return ApproveResult{}, fmt.Errorf("spec branch: %w", err)
	}

	// 2. The sub-tasks, in one transaction with the approval.
	res := ApproveResult{Created: []string{}, Updated: []string{}, Deleted: []string{}, Kept: []string{}}
	touched := e.touchedTasks()
	err = e.st.Tx(ctx, func(q *db.Queries) error {
		r, err := q.ApproveSpec(ctx, db.ApproveSpecParams{Branch: branch, CommitSha: commit, Path: file,
			ApprovedAt: nullTime(now()), ID: id})
		if err != nil {
			return invalid("version %d was approved or replaced meanwhile", s.Version)
		}
		if err := q.SupersedeApprovedSpecs(ctx, db.SupersedeApprovedSpecsParams{GoalID: goal.ID, ID: id}); err != nil {
			return err
		}
		res.Spec = specFromRow(r)
		return e.applyPlanLocked(ctx, q, goal, s.Plan, touched, &res)
	})
	if err != nil {
		return ApproveResult{}, err
	}
	e.publishSpecs(ctx, goal.ID)
	if tasks, err := e.q.ListChildTasks(ctx, nullString(goal.ID)); err == nil {
		for _, t := range tasks {
			v := taskFromRow(t)
			e.publish(Update{Type: "task", Task: &v})
		}
	}
	for _, d := range res.Deleted {
		e.publish(Update{Type: "task_deleted", ID: d})
	}
	e.log.Info("plan approved", "task_id", goal.ID, "spec_id", id, "version", s.Version, "commit", short(commit),
		"created", len(res.Created), "updated", len(res.Updated), "deleted", len(res.Deleted), "kept", len(res.Kept))

	// 3. The architect's job is done; the Tech Lead executes the plan.
	e.mu.Lock()
	var arch []string
	for _, a := range e.agents {
		if a.info.Kind == KindArchitect && a.info.TaskID == goal.ID && a.running() {
			arch = append(arch, a.info.ID)
		}
	}
	lead := e.leadOfGoalLocked(goal.ID)
	e.mu.Unlock()
	for _, aid := range arch {
		e.wg.Add(1)
		go func() { defer e.wg.Done(); _ = e.stop(aid, ExitPlanApproved) }()
	}
	if lead != nil {
		body := fmt.Sprintf("The user approved version %d of the plan. Created sub-tasks: %s. Updated: %s. Deleted: %s. "+
			"Unchanged because work started: %s. Call list_tasks and continue in dependency order.", s.Version,
			listOrNone(res.Created), listOrNone(res.Updated), listOrNone(res.Deleted), listOrNone(res.Kept))
		if _, err := e.sendMessage(ctx, "", lead.info.ID, body, false); err != nil {
			e.log.Warn("tell the lead about the plan", "err", err)
		}
		v := lead.view()
		res.Lead = &v
		return res, nil
	}
	a, err := e.Start(ctx, StartRequest{TaskID: goal.ID, Role: RoleTechLead})
	if err != nil {
		res.LeadError = err.Error()
		e.log.Warn("start the Tech Lead after approval", "task_id", goal.ID, "err", err)
	} else {
		res.Lead = &a
	}
	return res, nil
}

func listOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// touchedTasks returns the tasks any agent ever worked on (they are kept
// when a revised plan changes them).
func (e *Engine) touchedTasks() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]bool{}
	for _, a := range e.agents {
		if a.info.TaskID != "" && a.info.Kind == KindWorker {
			out[a.info.TaskID] = true
		}
	}
	return out
}

// applyPlanLocked creates, updates and deletes the goal's sub-tasks to match
// an approved plan: new keys are created; untouched todo sub-tasks are updated
// or, when dropped from the plan, deleted; anything already worked on stays.
func (e *Engine) applyPlanLocked(ctx context.Context, q *db.Queries, goal db.Task, p Plan, touched map[string]bool,
	res *ApproveResult) error {
	existing, err := q.ListChildTasks(ctx, nullString(goal.ID))
	if err != nil {
		return err
	}
	byKey := map[string]db.Task{}
	for _, t := range existing {
		if t.SpecKey != "" {
			byKey[t.SpecKey] = t
		}
	}
	untouched := func(t db.Task) bool { return t.Status == StatusTodo && !touched[t.ID] }
	inPlan := map[string]bool{}
	for _, st := range p.SubTasks {
		inPlan[st.Key] = true
	}
	// Drop untouched sub-tasks the plan no longer has.
	remaining := 0
	for _, t := range existing {
		switch {
		case t.SpecKey != "" && !inPlan[t.SpecKey] && untouched(t):
			if _, err := q.RemoveTask(ctx, t.ID); err != nil {
				return err
			}
			res.Deleted = append(res.Deleted, t.ID)
			delete(byKey, t.SpecKey)
		default:
			remaining++
		}
	}
	created := 0
	for _, st := range p.SubTasks {
		if _, ok := byKey[st.Key]; !ok {
			created++
		}
	}
	if remaining+created > MaxSubTasks {
		return fmt.Errorf("%w: the goal would have %d sub-tasks; at most %d", ErrLimit, remaining+created, MaxSubTasks)
	}
	// Create new keys first (without dependencies), so every key has an id.
	ids := map[string]string{}
	for k, t := range byKey {
		ids[k] = t.ID
	}
	t0 := now()
	for _, st := range p.SubTasks {
		if _, ok := byKey[st.Key]; ok {
			continue
		}
		row, err := q.CreateTask(ctx, db.CreateTaskParams{ID: randomHex(6), ProjectID: e.project.ID, Title: st.Title,
			Description: st.Description, Status: StatusTodo, CreatedAt: t0, UpdatedAt: t0,
			ParentTaskID: nullString(goal.ID), CreatedBy: "", DependsOn: "[]", Files: jsonText(st.Files),
			Acceptance: jsonText(st.Acceptance), Role: st.Role, SpecKey: st.Key})
		if err != nil {
			return err
		}
		ids[st.Key] = row.ID
		res.Created = append(res.Created, row.ID)
	}
	for _, st := range p.SubTasks {
		var deps []string
		for _, d := range st.DependsOn {
			deps = append(deps, ids[d])
		}
		t, existed := byKey[st.Key]
		id := ids[st.Key]
		if existed && !untouched(t) {
			res.Kept = append(res.Kept, id)
			continue
		}
		cur, err := q.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if _, err := q.UpdateTaskPlan(ctx, db.UpdateTaskPlanParams{Title: st.Title, Description: st.Description,
			DependsOn: jsonText(deps), Files: jsonText(st.Files), Acceptance: jsonText(st.Acceptance), Role: st.Role,
			UpdatedAt: now(), ID: cur.ID}); err != nil {
			return err
		}
		if existed {
			res.Updated = append(res.Updated, id)
		}
	}
	return nil
}

// goalBase is the commit the goal's work starts from: its approved spec's
// commit, or "" for the main repository's HEAD.
func (e *Engine) goalBase(ctx context.Context, goalID string) string {
	s, err := e.q.LatestApprovedSpec(ctx, goalID)
	if err != nil {
		return ""
	}
	return s.CommitSha
}

// planSection tells a Tech Lead about its goal's approved plan.
func (e *Engine) planSection(ctx context.Context, goal *db.Task) string {
	s, err := e.q.LatestApprovedSpec(ctx, goal.ID)
	if err != nil {
		return ""
	}
	children, err := e.q.ListChildTasks(ctx, nullString(goal.ID))
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "An approved plan exists (version %d, %s on branch %s; every sub-task's branch starts from it). "+
		"Its sub-tasks are already created; assign them in dependency order:\n", s.Version, s.Path, s.Branch)
	for _, c := range children {
		v := taskFromRow(c)
		fmt.Fprintf(&b, "- %s [%s] %s (%s)", v.ID, v.SpecKey, v.Title, v.Status)
		if len(v.DependsOn) > 0 {
			fmt.Fprintf(&b, "; depends on %s", strings.Join(v.DependsOn, ", "))
		}
		if v.Role != "" {
			fmt.Fprintf(&b, "; role %s", v.Role)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// relaunchArchitect starts an exited architect again for the Tech Lead's
// escalation (ADR 0031): same agent and workspace, a new session whose prompt
// is the approved plan. The lead's message is delivered after its first turn.
func (e *Engine) relaunchArchitect(ctx context.Context, id, lead string) (string, error) {
	e.mu.Lock()
	a, ok := e.agents[id]
	var info AgentInfo
	if ok {
		info = a.info
	}
	e.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	goal, err := e.getTask(ctx, info.TaskID)
	if err != nil {
		return "", err
	}
	if goal.Status == StatusDone {
		return "", invalid("the goal is done")
	}
	def := e.roleOf(info.Role, info.Kind)
	var b strings.Builder
	b.WriteString(e.expandPrompt(def) + "\n\n" + goalSection(&goal))
	if s, err := e.q.LatestApprovedSpec(ctx, goal.ID); err == nil {
		fmt.Fprintf(&b, "\nThe user approved version %d of your plan earlier:\n\n%s\n", s.Version, s.Markdown)
	}
	b.WriteString("\nYou were started again because the goal's Tech Lead escalated a problem with the plan. Look at the " +
		"current state with list_tasks, then end your turn: the Tech Lead's message is delivered to you right after. " +
		"Then either answer it with send_message(to=\"lead\"), or revise the plan with submit_plan (the user approves " +
		"it again; work already started stays as it is) and tell the lead. Ask the user with message_user if a " +
		"decision is theirs.")
	if _, err := e.relaunch(ctx, id, b.String(), nil); err != nil {
		return "", fmt.Errorf("start the architect again: %w", err)
	}
	e.log.Info("architect relaunched for an escalation", "agent_id", id, "task_id", goal.ID, "lead", lead)
	return id, nil
}
