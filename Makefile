# agent-office build tasks. `make build` produces bin/agent-office with the UI embedded.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# sqlc runs pinned via `go run` so its large dependency tree stays out of go.mod.
SQLC_VERSION ?= v1.31.1
# Development runs and tests never use the real data dir (~/.local/share/agent-office).
# Outside the repo on purpose: Claude Code loads CLAUDE.md from parent folders, so
# worktrees under this repo would pick up agent-office's own rules.
DEV_ROOT ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/agent-office-dev
DEV_DATA_DIR ?= $(DEV_ROOT)/data

.PHONY: build web test web-test ui-test lint check dev serve clean generate e2e sandbox-test worker-image

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
	AO_E2E_REPO="$(AO_E2E_REPO)" AO_E2E_GITHUB_REPO="$(AO_E2E_GITHUB_REPO)" AO_E2E_PACK="$(AO_E2E_PACK)" go test -tags e2e -count=1 -v -timeout 40m ./test/e2e/

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

# Run the built daemon against DEV_DATA_DIR, never the real data dir.
#   make serve REPO=~/projects/agent-office-playground [ARGS="--dev --addr 127.0.0.1:7690"]
serve: build
	@test -n "$(REPO)" || (echo "set REPO to a git repository"; exit 1)
	./bin/agent-office serve --data-dir "$(DEV_DATA_DIR)" --repo "$(REPO)" $(ARGS)

# Development: run the daemon with --dev and Vite with hot reload.
#   terminal 1: make serve REPO=<path> ARGS=--dev
#   terminal 2: make dev   (then open http://localhost:5173)
dev: web/node_modules
	cd web && npm run dev

# Regenerate internal/store/db from internal/store/{migrations,queries.sql}.
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

clean:
	rm -rf bin web/dist/app
