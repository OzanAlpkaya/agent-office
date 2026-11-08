# 0041. Reproducible worker images: a pinned Debian snapshot, pinned versions, python3

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

The worker images (ADR 0007, 0023) pin the Debian base image by digest and the Go download by
SHA-256, but `apt-get install git curl ...` installed whatever `deb.debian.org` served that day. Two
builds of the same Containerfile could differ, and the image tag, which is derived from the
Containerfile, would not tell. Phase 11 backlog: pin the Debian packages. A second backlog item from
Ozan's end-to-end test: agents reach for `python3` to edit files and fail, because the image has no
Python.

## Options considered

1. **Pin versions only** (`git=1:2.47.3-0+deb13u1`) against the live mirror: explicit, but Debian
   removes superseded versions from the mirror when a point release or security update lands, so the
   build breaks within weeks; dependencies of the pinned packages still float.
2. **A snapshot of the archive** (`snapshot.debian.org/archive/debian/<timestamp>`): every package,
   dependencies included, comes from one frozen state of the archive, so the same Containerfile
   installs the same packages for as long as the snapshot service exists. Builds are slower; the
   service rate-limits heavy use.
3. **Vendoring `.deb` files** or a local mirror: fully offline, but large binary files in the
   repository or another service to run.
4. **Python: add it, or tell agents it is missing.** Telling agents (in the packs' skills) does not
   help an agent without a pack, and agents fall back to Python for multi-line edits regardless.

## Decision

Options 2 and 1 together, and add `python3`.

- The base image rewrites its apt sources to the snapshot timestamp the Debian image itself was built
  from (the image's sources file names it: `20261104T212339Z`), for `debian` and `debian-security`,
  and fails the build if the rewrite did not apply. `Acquire::Check-Valid-Until` is off, because a
  snapshot's `Release` file expires by design; signatures are still verified with the image's
  keyring.
- Directly installed packages are pinned to their version in that snapshot (`pkg=version`), so a
  change is visible in the diff and a wrong version fails loudly. Their dependencies are fixed by the
  snapshot.
- Pack images build `FROM` the base image and inherit its sources, so the built-in `go` and `node`
  packs pin their packages from the same snapshot.
- `python3` (Debian's, with the standard library, no pip) is in the base image: about 35 MB more
  (192 MB to 227 MB).
- Updating is deliberate: a newer base image digest, its snapshot timestamp, then the versions
  (`apt-cache policy <pkg>` in a container with those sources). The image tags change with the
  Containerfiles, so workers pick up the new images after `agent-office worker-image`.

Checked: a `--no-cache` rebuild of the base image installs the same 128 packages with the same
versions (`dpkg-query -W`); `TestWorkerImageAndInit` checks python3, the snapshot sources and git's
version; `make sandbox-test` passes on the rebuilt base, `go` and `node` images.

## Consequences

- Same Containerfile, same packages, independent of the build day.
- Security updates no longer arrive by rebuilding: they need a snapshot bump. That is the trade-off
  of reproducibility; the containers have no network but the egress proxy and run as an
  unprivileged user, and the bump is a small, reviewable change.
- Building depends on `snapshot.debian.org` (slower than the mirror, occasionally rate-limited). A
  failed build is loud; images are built rarely and kept by tag.
- Agents can use Python for edits and scripts in every image. Packages from PyPI still need the
  pack's egress allowlist to include it.
