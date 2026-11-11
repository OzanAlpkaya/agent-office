---
name: go-sandbox
description: How to build, test and format Go code in this agent-office sandbox (Go 1.27.3, race detector available, modules only through the Go module proxy). Use before running go commands or adding dependencies.
---

# Go in the agent-office sandbox

You run in a network-restricted container. What that means for Go:

- **Toolchain:** Go 1.27.3 with cgo (`CGO_ENABLED=1`), so `go test -race` works.
  `GOTOOLCHAIN=local`: Go never downloads another toolchain. If `go.mod` asks for a newer `go` or
  `toolchain` version, do not raise it; say so in your report instead.
- **Modules:** only `proxy.golang.org` and `sum.golang.org` are reachable (plus
  `storage.googleapis.com`, where the proxy serves some module zips from). `GOPROXY=direct`,
  `GOPRIVATE`, `GONOSUMDB` and fetching from GitHub or other VCS hosts fail. Prefer the standard
  library; add a dependency only if the task needs it, and keep `go.sum` committed.
- **Caches** (`GOPATH`, `GOCACHE`) live under `$HOME`, which is temporary: the first build of a
  session is slower.
- **Memory** is limited per worker. If a command prints `Killed` or exits with signal 9, it ran out
  of memory: run fewer packages at once (`go test -p 1 ./...`) instead of retrying the same command.
- **make** is installed, for the project's own `Makefile` targets.
- **Before you commit**, run and fix:
  ```sh
  gofmt -l .        # must print nothing
  go vet ./...
  go test -race ./...
  ```
