# 0042. Releases: a tagged, clean tree, one static linux/amd64 binary, uploaded by hand

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

Phase 11 ends with the first release, v0.1.0. The binary embeds the UI (ADR 0005) and the worker
image's build files, and is static (`CGO_ENABLED=0`) because it is also mounted into worker
containers (Spike F). Ozan does not use the `gh` CLI, and the project never does (CLAUDE.md), so a
release is created on the GitHub website. There is no CI. The repository is public.

## Options considered

1. **Build from the working copy** with `make build` and attach the file: simple, but a dirty tree,
   local `node_modules` or an untagged commit end up in the release without anyone noticing.
2. **Build from a fresh worktree of an exact tag** (`make release`): the binary is what the tag
   holds, `agent-office version` prints the tag, the UI is built from `npm ci`; packaged as a
   deterministic tarball with a checksum file.
3. **GoReleaser or a CI release workflow:** cross-platform builds, signing and the upload itself,
   but a new tool and either the `gh`-style API upload with a token or a workflow under `.github/`.
4. **Platforms:** linux/amd64 only, or also linux/arm64 and macOS. The daemon is Linux-first
   (rootless Podman, PTYs, `XDG_RUNTIME_DIR`); only linux/amd64 is tested.

## Decision

Option 2, linux/amd64 only.

- `make release` refuses a dirty tree, a commit without an exact tag and a tag without release notes
  in `docs/releases/<tag>.md`. It builds in `dist/src`, a fresh `git worktree` of the tag
  (`npm ci`, `make build`), and checks that `agent-office version` prints the tag.
- `dist/agent-office_<tag>_linux_amd64.tar.gz` holds the binary, `LICENSE`, `README.md` and the
  notes as `RELEASE-NOTES.md`. tar runs with a fixed order, owner and mtime (the tagged commit's
  time) and gzip without a timestamp, so the same tag gives the same tarball given the same
  toolchain. `dist/SHA256SUMS` carries its checksum.
- Before a release, `make secret-scan` (gitleaks over every commit) runs; the public repository is
  what gets published, not only the binary.
- Tags are annotated, `vMAJOR.MINOR.PATCH`; `0.x` while the project is pre-1.0 (flags, API and
  database schema may change, migrations stay forward-only). A tag is created locally; Ozan pushes
  it and creates the release on the website from the notes, uploading the tarball and
  `SHA256SUMS`.
- v0.1.0 is tagged on the Phase 11 branch's last commit. The `--no-ff` merge keeps it in `main`'s
  history with the same tree.

## Consequences

- A release binary always matches a tag and a clean tree; anyone can rebuild it from the tag and
  compare the checksum (the Go and Node toolchain versions are in the notes).
- Publishing stays a manual, reviewed step. A CI workflow can automate the same target later; it
  would be a change under `.github/`, which the daemon gates for agents' pushes (ADR 0020).
- No arm64 or macOS binaries. Building them is one `GOARCH`/`GOOS` away, but untested builds are
  not shipped.
- The binary is unsigned; the checksum detects corruption, not a malicious upload. Signing (for
  example with Sigstore) is a later option.
