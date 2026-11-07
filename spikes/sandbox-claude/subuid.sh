#!/usr/bin/env bash
# Spike F addendum: agent clone owned by a subordinate uid instead of the host user
# (no --userns=keep-id). Does git's safe.directory check protect host tools from
# agent-written git config, and what does it cost? Output in out/subuid (gitignored).
set -euo pipefail
cd "$(dirname "$0")"
O=$PWD/out/subuid IMG=localhost/ao-spike-worker
podman unshare rm -rf "$O" 2>/dev/null || true
mkdir -p "$O"
export GIT_CONFIG_GLOBAL=/dev/null GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid \
  GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
MARK=$O/PWNED
say() { printf '%-62s %s\n' "$1" "$2"; }
trap_fired() { if [ -e "$MARK" ]; then rm -f "$MARK"; echo "TRAP FIRED"; else echo "no trap"; fi; }

# Main repo and a shared clone, as internal/git.CreateSharedClone builds it.
git init -q -b main "$O/origin" && echo hi > "$O/origin/README" && git -C "$O/origin" add README \
  && git -C "$O/origin" commit -qm init
BASE=$(git -C "$O/origin" rev-parse HEAD) OBJ=$O/origin/.git/objects C=$O/clone
git init -q --template= "$C" && mkdir -p "$C/.git/objects/info" && echo "$OBJ" > "$C/.git/objects/info/alternates"
git -C "$C" update-ref refs/heads/agent-office/x "$BASE" && git -C "$C" symbolic-ref HEAD refs/heads/agent-office/x
git -C "$C" reset -q --hard

# Hand it to container uid 1000 (host: subordinate uid), from inside the user namespace.
podman unshare chown -R 1000:1000 "$C"
say "clone owner on host" "$(stat -c '%u:%g' "$C" "$C/.git" | tr '\n' ' ')"

RUN=(podman run --rm --pull=never --network none --user 1000:1000 --read-only --cap-drop=all
  --security-opt no-new-privileges -e HOME=/tmp -e GIT_AUTHOR_NAME=Ozan -e GIT_AUTHOR_EMAIL=ozan@example.invalid
  -e GIT_COMMITTER_NAME=Ozan -e GIT_COMMITTER_EMAIL=ozan@example.invalid
  -v "$C:/workspace:rw" -w /workspace -v "$OBJ:$OBJ:ro" "$IMG")

# The agent commits and plants the trap (a host path it could guess).
"${RUN[@]}" sh -c "echo work >> README && git add README && git commit -qm 'feat: work' \
  && git config core.fsmonitor 'touch $MARK' && git log --oneline | wc -l" >/dev/null \
  && say "agent commit inside container (objects via ro alternates)" "ok"
say "files the agent wrote, owner on host" "$(stat -c '%u' "$C/README" "$C/.git/config" | sort -u | tr '\n' ' ')"
"${RUN[@]}" git status --porcelain >/dev/null; say "git inside container (its own repo)" "$(trap_fired)"

# Host tools against the clone.
unset GIT_CONFIG_GLOBAL  # use the real (empty) safe.directory configuration
out=$(cd "$C" && git status 2>&1 || true)
say "host: git status in clone" "$(trap_fired) | $(echo "$out" | head -1 | cut -c1-48)"
(cd "$C" && git -c core.fsmonitor= log -1 >/dev/null 2>&1 || true); say "host: git log in clone" "$(trap_fired)"
(cd "$C/.git" && git rev-parse --git-dir >/dev/null 2>&1 || true); say "host: git inside clone/.git" "$(trap_fired)"
(cd "$C" && STARSHIP_CONFIG=/dev/null starship prompt >/dev/null 2>&1 || true); say "host: starship prompt in clone" "$(trap_fired)"
(cd "$C" && starship prompt >/dev/null 2>&1 || true); say "host: starship prompt in clone (your config)" "$(trap_fired)"
export GIT_CONFIG_GLOBAL=/dev/null

# Hand-off: bundle to stdout from a helper container, import on the host.
"${RUN[@]}" git bundle create - "$BASE..refs/heads/agent-office/x" > "$O/work.bundle" 2>/dev/null
git -C "$O/origin" bundle verify -q "$O/work.bundle" >/dev/null 2>&1 \
  && git -C "$O/origin" -c transfer.fsckObjects=true fetch -q --no-tags "$O/work.bundle" \
     +refs/heads/agent-office/x:refs/heads/agent-office/x \
  && say "bundle via helper stdout + host import" "ok ($(git -C "$O/origin" rev-list --count "$BASE..agent-office/x") commit)"
say "trap after import" "$(trap_fired)"

# Unix socket owned by the host user, connected to by container uid 1000.
python3 -c "import socket,os,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); os.chmod(sys.argv[1],0o600)" "$O/a.sock"
r=$("${RUN[@]/-v/-v}" 2>/dev/null true; podman run --rm --pull=never --network none --user 1000:1000 \
  -v "$O/a.sock:/a.sock" "$IMG" sh -c 'test -w /a.sock && echo writable || echo "not writable"')
say "socket 0600 owned by host user, as container uid 1000" "$r"
podman unshare chown 1000:1000 "$O/a.sock"
r=$(podman run --rm --pull=never --network none --user 1000:1000 -v "$O/a.sock:/a.sock" "$IMG" \
  sh -c 'test -w /a.sock && echo writable || echo "not writable"')
say "same socket after podman unshare chown 1000:1000" "$r ($(stat -c '%u %a' "$O/a.sock"))"

# Control: the same clone owned by the host user (what --userns=keep-id gives).
podman unshare cp -a "$C" "$O/control" && podman unshare chown -R 0:0 "$O/control"
unset GIT_CONFIG_GLOBAL
say "control clone owner on host" "$(stat -c '%u' "$O/control")"
(cd "$O/control" && git status >/dev/null 2>&1 || true); say "control: host git status" "$(trap_fired)"
(cd "$O/control" && starship prompt >/dev/null 2>&1 || true); say "control: starship prompt (your config)" "$(trap_fired)"
export GIT_CONFIG_GLOBAL=/dev/null
rm -rf "$O/control"

# Cleanup and reading as the host user.
rm -rf "$C" 2>/dev/null && say "host rm -rf clone" "ok" || say "host rm -rf clone" "fails (permission denied)"
cat "$C/README" >/dev/null 2>&1 && say "host can read agent files (umask 022 in container)" "yes" || say "host can read agent files" "no"
podman unshare rm -rf "$C" && say "podman unshare rm -rf clone" "ok"
