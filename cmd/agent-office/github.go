package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/OzanAlpkaya/agent-office/internal/engine"
	"github.com/OzanAlpkaya/agent-office/internal/git"
	"github.com/OzanAlpkaya/agent-office/internal/github"
	"github.com/OzanAlpkaya/agent-office/internal/review"
)

// githubOptions checks everything the GitHub loop needs (ADR 0020) and fails
// loudly, with the fix, when something is missing: the token file, a GitHub
// push remote, and a token that can read the repository.
// githubSpec is a project's effective GitHub settings.
type githubSpec struct {
	repo, remote, reviewer string
	rounds                 int
}

func githubOptions(ctx context.Context, cfg githubSpec, g git.Git, log *slog.Logger) (*engine.GitHubOptions, error) {
	hint := "or turn GitHub off for this project"
	tokenFile, err := github.DefaultTokenFile()
	if err != nil {
		return nil, err
	}
	top, err := g.TopLevel(ctx, cfg.repo)
	if err != nil {
		return nil, fmt.Errorf("repository: %w", err)
	}
	remoteURL, err := g.RemoteURL(ctx, top, cfg.remote)
	if err != nil {
		return nil, fmt.Errorf("GitHub pushes to the remote %q of %s: %w; %s", cfg.remote, top, err, hint)
	}
	repo, err := github.ParseRemote(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("GitHub remote %s: %w; %s", cfg.remote, err, hint)
	}
	client, err := github.New(github.Options{Repo: repo, TokenFile: tokenFile, Logger: log.With("component", "github")})
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := client.Repository(cctx)
	if err != nil {
		return nil, fmt.Errorf("GitHub repository %s: %w\nthe daemon reads a fine-grained token for that repository "+
			"from %s (mode 0600, directory 0700; Metadata read, Pull requests read/write, Commit statuses and Checks "+
			"read; see docs/adr/0020-github-integration.md); %s", repo, err, tokenFile, hint)
	}
	log.Info("GitHub ready", "repo", info.FullName, "default_branch", info.DefaultBranch, "private", info.Private,
		"remote", cfg.remote, "reviewer", cfg.reviewer, "review_rounds", cfg.rounds, "token_file", tokenFile)
	return &engine.GitHubOptions{Client: client, Remote: cfg.remote, Base: info.DefaultBranch,
		Reviewer: cfg.reviewer, CodeRabbitLogin: review.CodeRabbitLogin, Rounds: cfg.rounds}, nil
}
