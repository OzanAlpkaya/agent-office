package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// Only open sub-tasks count against the project's limit; reported, done and
// superseded ones do not (ADR 0062).
func TestSubTaskLimitCountsOpenOnly(t *testing.T) {
	tf := newTeamWith(t, func(o *Options) { o.MaxSubTasks = 3 })
	ctx := context.Background()
	var ids []string
	for i := range 3 {
		ti, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: fmt.Sprintf("t%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ti.ID)
	}
	_, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "fourth"})
	if !isUser(err, ErrLimit) || !strings.Contains(err.Error(), "3 open sub-tasks") {
		t.Fatalf("over the limit: %v", err)
	}
	// A reported and a done sub-task free their places.
	if _, err := tf.e.UpdateTask(ctx, ids[0], TaskPatch{Status: Ptr(StatusReview)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.e.UpdateTask(ctx, ids[1], TaskPatch{Status: Ptr(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"fourth", "fifth"} {
		if _, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: title}); err != nil {
			t.Fatalf("%s: %v", title, err)
		}
	}
	// Full again; a replacement that supersedes an open one fits.
	if _, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "sixth"}); !isUser(err, ErrLimit) {
		t.Fatalf("full again: %v", err)
	}
	repl, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "t2, done right", Supersedes: []string{ids[2]}})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := tf.e.getTask(ctx, ids[2])
	if old.Status != StatusDone || old.SupersededBy != repl.ID {
		t.Fatalf("superseded task %+v", taskFromRow(old))
	}
	if ll, _ := tf.team.ListTasks(ctx, tf.lead.ID); !strings.Contains(fmt.Sprint(ll.Tasks), repl.ID) {
		t.Fatal("replacement not listed")
	}
	// The limit is a live project setting.
	tf.e.setLiveForTest(func(l *liveOptions) { l.MaxSubTasks = 5 })
	if _, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "sixth"}); err != nil {
		t.Fatalf("after raising the limit: %v", err)
	}
	if !strings.Contains(tf.e.expandPrompt(tf.e.lv().Roles.Default("lead")), "at most 5 open") {
		t.Fatal("the lead's prompt does not name the project's limit")
	}
}

func TestSupersedeRefusals(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	running, _ := tf.worker(t, "busy")
	done, _ := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "done"})
	if _, err := tf.e.UpdateTask(ctx, done.ID, TaskPatch{Status: Ptr(StatusDone)}); err != nil {
		t.Fatal(err)
	}
	other, err := tf.e.CreateTask(ctx, TaskInput{Title: "another goal"})
	if err != nil {
		t.Fatal(err)
	}
	for name, ids := range map[string][]string{
		"running agent": {running.ID}, "done": {done.ID}, "not a sub-task": {other.ID}, "unknown": {"nope"},
	} {
		if _, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "x", Supersedes: ids}); !isUser(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ll, _ := tf.team.ListTasks(ctx, tf.lead.ID); len(ll.Tasks) != 2 {
		t.Fatalf("a refused create_task created a task: %d", len(ll.Tasks))
	}
}

// A dependency on a superseded sub-task waits for, and starts from, its
// replacement.
func TestDependencyFollowsReplacement(t *testing.T) {
	tf := newTeam(t)
	ctx := context.Background()
	a, _ := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "a"})
	b, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "b", DependsOn: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := tf.team.CreateTask(ctx, tf.lead.ID, mcp.CreateTaskInput{Title: "a again", Supersedes: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	bt, _ := tf.e.getTask(ctx, b.ID)
	unmet, err := tf.e.unmetDependencies(ctx, bt)
	if err != nil || len(unmet) != 1 || !strings.Contains(unmet[0], a2.ID) {
		t.Fatalf("b waits for %v (%v), want the replacement %s", unmet, err, a2.ID)
	}
	if _, err := tf.e.UpdateTask(ctx, a2.ID, TaskPatch{Status: Ptr(StatusReview)}); err != nil {
		t.Fatal(err)
	}
	if unmet, _ := tf.e.unmetDependencies(ctx, bt); len(unmet) != 0 {
		t.Fatalf("b still waits: %v", unmet)
	}
}
