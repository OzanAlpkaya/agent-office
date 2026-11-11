# agent-office build tasks. `make build` produces bin/agent-office with the UI embedded.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# sqlc runs pinned via `go run` so its large dependency tree stays out of go.mod.
SQLC_VERSION ?= v1.31.1
# gitleaks runs pinned via `go run` too (secret scan of the whole history, Phase 11).
GITLEAKS_VERSION ?= v8.30.1
# Development runs and tests never use the real data dir (~/.local/share/agent-office).
# Outside the repo on purpose: Claude Code loads CLAUDE.md from parent folders, so
# worktrees under this repo would pick up agent-office's own rules.
DEV_ROOT ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/agent-office-dev
DEV_DATA_DIR ?= $(DEV_ROOT)/data

.PHONY: release demo secret-scan pair build web test web-test ui-test lint check dev serve clean generate e2e sandbox-test worker-image

# Static (CGO_ENABLED=0): the same binary is mounted into worker containers, whose
# glibc may be older than the host's (Spike F).
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/agent-office ./cmd/agent-office

web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci --no-audit --no-fund
	touch web/node_modules

# Tests use temp dirs; this also points the default data dir at scratch in case one forgets.
test:
	AGENT_OFFICE_DATA_DIR= XDG_DATA_HOME="$(DEV_ROOT)/test-xdg" go test -race ./...

lint: web/node_modules
	@test -z "$$(gofmt -l cmd internal web)" || (gofmt -l cmd internal web; exit 1)
	go vet ./...
	cd web && npm run typecheck

# Unit tests of the UI's pure logic (Vitest, ADR 0025).
web-test: web/node_modules
	cd web && npm test

check: lint test web-test build

# Opt-in end-to-end test with real Claude Code sessions (haiku; costs a few cents).
# The test passes --data-dir with a temp dir to every daemon it starts. TestEndToEnd runs host
# mode in AO_E2E_REPO; TestSandboxEndToEnd runs a sandboxed agent on a throwaway clone of it and
# needs podman, the OAuth token file (ADR 0015) and the image of the stack pack AO_E2E_PACK
# (default go, the playground's stack: make worker-image PACK=go). TestGitHubEndToEnd also
# pushes to GitHub and opens a PR there, only when AO_E2E_GITHUB_REPO (owner/name, the push remote
# of AO_E2E_REPO) is set; it needs the GitHub token file (ADR 0020). AO_E2E_KEEP=1 keeps its PR.
#   AO_E2E_REPO=~/projects/agent-office-playground make e2e
#   AO_E2E_REPO=... AO_E2E_GITHUB_REPO=OzanAlpkaya/agent-office-playground make e2e
e2e:
	@test -n "$(AO_E2E_REPO)" || (echo "set AO_E2E_REPO to a trusted git repository"; exit 1)
	AO_E2E_REPO="$(AO_E2E_REPO)" AO_E2E_GITHUB_REPO="$(AO_E2E_GITHUB_REPO)" AO_E2E_PACK="$(AO_E2E_PACK)" AO_E2E_REVIEWER_CLI="$(AO_E2E_REVIEWER_CLI)" go test -tags e2e -count=1 -v -timeout 40m ./test/e2e/

# Build the base worker image, and a stack pack's image with PACK=<name> (podman; needs network).
# Tags come from internal/sandbox/Containerfile and the pack's files (ADR 0023).
#   make worker-image PACK=go
worker-image: build
	./bin/agent-office worker-image $(if $(PACK),--pack $(PACK))

# Opt-in integration tests against real rootless Podman (no tokens; needs the pinned base image,
# `make worker-image` for the worker tests, and the go and node pack images for the toolchain
# tests: make worker-image PACK=go; make worker-image PACK=node). AO_SANDBOX_NET=1 adds HTTPS
# through the egress proxy, which needs internet access.
sandbox-test: build
	go test -tags sandbox -race -count=1 -v ./internal/sandbox/ ./internal/engine/

# Opt-in browser tests of the UI against a mocked daemon (Playwright, ADR 0025); no daemon, no
# tokens. Screenshots land in web/test-results/screens. Needs the browser once:
#   cd web && npx playwright install chromium-headless-shell
ui-test: web
	cd web && npm run ui-test

# Record the README's demo: a real sandboxed haiku team (Tech Lead and two workers) on a throwaway
# clone of AO_DEMO_REPO, with a scratch data dir; costs a few cents of tokens. Needs podman, the
# OAuth token file, the go pack image (make worker-image PACK=go) and Playwright's Chromium. The
# video, stills and the GIF land in AO_DEMO_OUT (docs/demo.md).
#   make demo AO_DEMO_REPO=~/projects/agent-office-playground AO_DEMO_OUT=/tmp/demo
demo: build
	@test -n "$(AO_DEMO_REPO)" || (echo "set AO_DEMO_REPO to a trusted git repository"; exit 1)
	@test -n "$(AO_DEMO_OUT)" || (echo "set AO_DEMO_OUT to an output directory"; exit 1)
	cd web && AO_BIN=../bin/agent-office AO_DEMO_REPO="$(AO_DEMO_REPO)" AO_DEMO_OUT="$(AO_DEMO_OUT)" node demo/record-team-run.ts
	web/demo/to-gif.sh "$(AO_DEMO_OUT)" "$(AO_DEMO_OUT)/team-run.gif"

# Run the built daemon against DEV_DATA_DIR, never the real data dir.
#   make serve REPO=~/projects/agent-office-playground [ARGS="--dev --addr 127.0.0.1:7690"]
serve: build
	@test -n "$(REPO)" || (echo "set REPO to a git repository"; exit 1)
	./bin/agent-office serve --data-dir "$(DEV_DATA_DIR)" --repo "$(REPO)" $(ARGS)

# Pair a browser with the daemon of `make serve` (ADR 0039); prints a one-time code and link.
pair:
	./bin/agent-office pair --data-dir "$(DEV_DATA_DIR)"

# Development: run the daemon with --dev and Vite with hot reload.
#   terminal 1: make serve REPO=<path> ARGS=--dev
#   terminal 2: make dev   (then open http://localhost:5173)
dev: web/node_modules
	cd web && npm run dev

# Scan every commit of every ref and the working tree for secrets (gitleaks, redacted output).
# The repository is public: run it before a release and after adding fixtures or captured data.
secret-scan:
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) git --log-opts=--all --redact --no-banner .
	go run github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION) dir --redact --no-banner .

# Release build (ADR 0042). HEAD must be a clean commit with an exact tag and release notes in
# docs/releases/<tag>.md. Builds in a fresh worktree of the tag (dist/src: npm ci, make build), so
# nothing local leaks in, and writes dist/agent-office_<tag>_linux_amd64.tar.gz (binary, LICENSE,
# README, RELEASE-NOTES.md) and dist/SHA256SUMS. The tarball is deterministic: fixed file order,
# owner and mtime (the tagged commit's time), gzip without a timestamp. Upload both on GitHub.
release:
	@test -z "$$(git status --porcelain)" || (echo "release: the working tree is not clean"; exit 1)
	@git describe --tags --exact-match >/dev/null 2>&1 || (echo "release: HEAD has no tag; git tag -a vX.Y.Z first"; exit 1)
	@test -f "docs/releases/$$(git describe --tags --exact-match).md" || (echo "release: docs/releases/<tag>.md is missing"; exit 1)
	rm -rf dist/src dist/stage
	git worktree prune
	git worktree add -q --detach dist/src "$$(git describe --tags --exact-match)"
	$(MAKE) -C dist/src build
	@set -eu; tag=$$(git describe --tags --exact-match); name=agent-office_$${tag}_linux_amd64; \
	  test "$$(dist/src/bin/agent-office version)" = "$$tag"; \
	  mkdir -p "dist/stage/$$name"; \
	  cp dist/src/bin/agent-office dist/src/LICENSE dist/src/README.md "dist/stage/$$name/"; \
	  cp "dist/src/docs/releases/$$tag.md" "dist/stage/$$name/RELEASE-NOTES.md"; \
	  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$$(git log -1 --format=%ct "$$tag")" \
	    -C dist/stage -cf - "$$name" | gzip -n -9 > "dist/$$name.tar.gz"; \
	  cd dist && sha256sum "$$name.tar.gz" > SHA256SUMS && cat SHA256SUMS
	git worktree remove --force dist/src
	rm -rf dist/stage

# Regenerate internal/store/db from internal/store/{migrations,queries.sql}.
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

clean:
	rm -rf bin web/dist/app
