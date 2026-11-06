# agent-office build tasks. `make build` produces bin/agent-office with the UI embedded.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# sqlc runs pinned via `go run` so its large dependency tree stays out of go.mod.
SQLC_VERSION ?= v1.31.1

.PHONY: build web test lint check dev clean generate

build: web
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/agent-office ./cmd/agent-office

web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci --no-audit --no-fund
	touch web/node_modules

test:
	go test -race ./...

lint: web/node_modules
	@test -z "$$(gofmt -l cmd internal web)" || (gofmt -l cmd internal web; exit 1)
	go vet ./...
	cd web && npm run typecheck

check: lint test build

# Development: run the daemon with --dev and Vite with hot reload.
#   terminal 1: go run ./cmd/agent-office serve --dev --repo <path>
#   terminal 2: make dev   (then open http://localhost:5173)
dev: web/node_modules
	cd web && npm run dev

# Regenerate internal/store/db from internal/store/{migrations,queries.sql}.
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

clean:
	rm -rf bin web/dist/app
