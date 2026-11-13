package engine

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/mcp"
)

// The Tech Lead's view of its team's work (Phase 20, ADR 0066). Its own
// workspace is a read-only clone without the other branches, so it reads
// its sub-tasks' and QA's files through the daemon: git plumbing in the main
// repository, for the lead's own goal only, read-only, bounded.

const (
	maxListedFiles = 500
	maxImageBytes  = 3_750_000 // the model's image limit, about 5 MB once base64-encoded
	maxTextBytes   = 256 << 10
	sourceQA       = "qa"
)

// teamSource is the commit a lead reads from.
type teamSource struct {
	name, branch, commit string
}

// leadSource resolves source for lead caller: a sub-task of its goal (its
// worker's branch in the main repository) or "qa" (the goal's newest QA run
// with a head).
func (e *Engine) leadSource(ctx context.Context, caller, tool, source string) (teamSource, error) {
	_, goal, err := e.asLead(ctx, caller, tool)
	if err != nil {
		return teamSource{}, err
	}
	if source == sourceQA {
		runs, err := e.q.ListGoalQARuns(ctx, goal.ID)
		if err != nil {
			return teamSource{}, err
		}
		for _, r := range runs { // newest first
			if r.Head != "" {
				return teamSource{name: sourceQA, branch: r.Branch, commit: r.Head}, nil
			}
		}
		return teamSource{}, invalid("the goal has no QA run with a branch yet; start_qa first")
	}
	t, err := e.getTask(ctx, source)
	if errors.Is(err, ErrNotFound) || (err == nil && t.ParentTaskID.String != goal.ID) {
		return teamSource{}, invalid("source must be a sub-task id of your goal (list_tasks shows them) or \"qa\"")
	}
	if err != nil {
		return teamSource{}, err
	}
	branch, head, err := e.branchOfTask(ctx, t.ID)
	if err != nil {
		return teamSource{}, err
	}
	return teamSource{name: t.ID, branch: branch, commit: head}, nil
}

func (t team) ListFiles(ctx context.Context, caller string, in mcp.ListFilesInput) (mcp.ListFilesOutput, error) {
	out, err := t.e.listFiles(ctx, caller, in)
	return out, userErr(err)
}

func (e *Engine) listFiles(ctx context.Context, caller string, in mcp.ListFilesInput) (mcp.ListFilesOutput, error) {
	src, err := e.leadSource(ctx, caller, mcp.ToolListFiles, in.Source)
	if err != nil {
		return mcp.ListFilesOutput{}, err
	}
	dir, err := git.CleanRepoPath(in.Path)
	if err != nil {
		return mcp.ListFilesOutput{}, invalid("%v", err)
	}
	entries, more, err := e.git.ListTree(ctx, e.project.RepoPath, src.commit, dir, in.Recursive, maxListedFiles)
	if errors.Is(err, git.ErrNotFound) {
		return mcp.ListFilesOutput{}, invalid("%s has no %s", src.name, in.Path)
	}
	if err != nil {
		var ge *git.Error
		if errors.As(err, &ge) {
			return mcp.ListFilesOutput{}, err
		}
		return mcp.ListFilesOutput{}, invalid("%v", err)
	}
	out := mcp.ListFilesOutput{Source: src.name, Branch: src.branch, Commit: src.commit, Entries: []mcp.FileEntry{}, More: more,
		Next: "Read a file with read_file(source, path); images come back as images."}
	for _, en := range entries {
		fe := mcp.FileEntry{Path: en.Path, Type: "file"}
		switch en.Type {
		case "tree":
			fe.Type = "dir"
		case "commit":
			fe.Type = "submodule"
		default:
			fe.Size = max(en.Size, 0)
		}
		out.Entries = append(out.Entries, fe)
	}
	if more {
		out.Next = "More than 500 entries: list a narrower path."
	}
	return out, nil
}

func (t team) ReadFile(ctx context.Context, caller string, in mcp.ReadFileInput) (mcp.FileContent, error) {
	out, err := t.e.readFile(ctx, caller, in)
	return out, userErr(err)
}

func (e *Engine) readFile(ctx context.Context, caller string, in mcp.ReadFileInput) (mcp.FileContent, error) {
	src, err := e.leadSource(ctx, caller, mcp.ToolReadFile, in.Source)
	if err != nil {
		return mcp.FileContent{}, err
	}
	p, err := git.CleanRepoPath(in.Path)
	if err != nil || p == "" {
		return mcp.FileContent{}, invalid("path must name a file relative to the repository's top")
	}
	data, size, err := e.git.ReadBlob(ctx, e.project.RepoPath, src.commit, p, maxImageBytes)
	switch {
	case errors.Is(err, git.ErrNotFound):
		return mcp.FileContent{}, invalid("%s has no file %s (list_files shows what it has)", src.name, p)
	case err != nil:
		var ge *git.Error
		if errors.As(err, &ge) {
			return mcp.FileContent{}, err
		}
		return mcp.FileContent{}, invalid("%v", err)
	case data == nil:
		return mcp.FileContent{}, invalid("%s is %d bytes: larger than read_file returns (images up to %d, text up to %d)",
			p, size, maxImageBytes, maxTextBytes)
	}
	out := mcp.FileContent{Source: src.name, Branch: src.branch, Commit: src.commit, Path: p, Size: size}
	switch kind := http.DetectContentType(data); kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		out.MIMEType, out.Image = kind, data
		e.log.Info("lead read an image", "agent_id", caller, "source", src.name, "bytes", size)
		return out, nil
	default:
		if size > maxTextBytes {
			return mcp.FileContent{}, invalid("%s is %d bytes of %s: text is returned up to %d bytes", p, size, kind, maxTextBytes)
		}
		if !utf8.Valid(data) || containsNUL(data) {
			return mcp.FileContent{}, invalid("%s is %s, not text or an image read_file returns (PNG, JPEG, GIF, WebP)", p, kind)
		}
		out.Text = string(data)
		return out, nil
	}
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
