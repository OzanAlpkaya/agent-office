package engine

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OzanAlpkaya/agent-office/internal/mcp"
	"github.com/OzanAlpkaya/agent-office/internal/store/db"
)

// Phase 20 (ADR 0066): the Tech Lead reads its sub-tasks' and QA's files,
// renders included, from the main repository; nothing else.
func TestLeadReadsTeamFiles(t *testing.T) {
	tf := newTeam(t)
	e, ctx, tm := tf.e, context.Background(), tf.team
	sub, w := tf.worker(t, "Render the level")
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 100)...)
	if err := os.MkdirAll(filepath.Join(w.WorktreePath, "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"renders/level1.png": png, "renders/notes.md": []byte("# Level 1\n"),
		"renders/blob.bin": {0, 1, 2, 3}} {
		if err := os.WriteFile(filepath.Join(w.WorktreePath, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, w.WorktreePath, "add", "renders")
	gitIn(t, w.WorktreePath, "commit", "-q", "-m", "renders")

	ls, err := tm.ListFiles(ctx, tf.lead.ID, mcp.ListFilesInput{Source: sub.ID, Path: "renders"})
	if err != nil || len(ls.Entries) != 3 || ls.Branch != w.Branch || ls.Entries[1].Path != "renders/level1.png" ||
		ls.Entries[1].Size != int64(len(png)) {
		t.Fatalf("list %+v %v", ls, err)
	}
	top, err := tm.ListFiles(ctx, tf.lead.ID, mcp.ListFilesInput{Source: sub.ID})
	if err != nil || !containsEntry(top.Entries, "renders", "dir") {
		t.Fatalf("top %+v %v", top, err)
	}
	img, err := tm.ReadFile(ctx, tf.lead.ID, mcp.ReadFileInput{Source: sub.ID, Path: "renders/level1.png"})
	if err != nil || img.MIMEType != "image/png" || !bytes.Equal(img.Image, png) || img.Text != "" {
		t.Fatalf("image %+v %v", img.MIMEType, err)
	}
	txt, err := tm.ReadFile(ctx, tf.lead.ID, mcp.ReadFileInput{Source: sub.ID, Path: "./renders/notes.md"})
	if err != nil || txt.Text != "# Level 1\n" || txt.Image != nil {
		t.Fatalf("text %+v %v", txt, err)
	}
	for name, in := range map[string]mcp.ReadFileInput{
		"binary":         {Source: sub.ID, Path: "renders/blob.bin"},
		"missing":        {Source: sub.ID, Path: "renders/nope.png"},
		"a directory":    {Source: sub.ID, Path: "renders"},
		"outside":        {Source: sub.ID, Path: "../../etc/passwd"},
		"absolute":       {Source: sub.ID, Path: "/etc/passwd"},
		"no path":        {Source: sub.ID},
		"another goal's": {Source: tf.goal.ID, Path: "README.md"},
		"unknown":        {Source: "nope", Path: "README.md"},
		"no QA yet":      {Source: "qa", Path: "README.md"},
	} {
		if _, err := tm.ReadFile(ctx, tf.lead.ID, in); err == nil || !isUser(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Only the Tech Lead reads them.
	if _, err := tm.ReadFile(ctx, w.ID, mcp.ReadFileInput{Source: sub.ID, Path: "renders/notes.md"}); !isUser(err, ErrForbidden) {
		t.Fatalf("a worker read: %v", err)
	}

	// The goal's QA branch: its newest run with a head.
	head := gitIn(t, w.WorktreePath, "rev-parse", "HEAD")
	run, err := e.q.InsertQARun(ctx, db.InsertQARunParams{GoalID: tf.goal.ID, AgentID: sql.NullString{}, Branch: "agent-office/qa-x",
		Merged: "[]", CreatedAt: now(), UpdatedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.FinishQARun(ctx, db.FinishQARunParams{Status: QAPassed, Head: head, Bugs: "[]", UpdatedAt: now(), ID: run.ID}); err != nil {
		t.Fatal(err)
	}
	qa, err := tm.ReadFile(ctx, tf.lead.ID, mcp.ReadFileInput{Source: "qa", Path: "renders/level1.png"})
	if err != nil || qa.Branch != "agent-office/qa-x" || qa.Commit != head || qa.MIMEType != "image/png" {
		t.Fatalf("qa %+v %v", qa, err)
	}
	if !strings.HasPrefix(qa.Commit, head[:8]) {
		t.Fatal("commit")
	}
}

func containsEntry(es []mcp.FileEntry, path, typ string) bool {
	for _, e := range es {
		if e.Path == path && e.Type == typ {
			return true
		}
	}
	return false
}
