#!/usr/bin/env bash
# Spike F: Claude Code in a rootless Podman worker. Throwaway; see README.md.
#   ./run.sh build | probe | version | noauth | auth | inject | interactive
#   interactive: keys via `printf '\r' > out/pty.in`, screen via `out/spike screen out/pty.log`.
#   PRESEED=file.json starts interactive with that file as the config's .claude.json.
# Output goes to out/ (gitignored). Logs never contain credentials.
set -euo pipefail
cd "$(dirname "$0")"
OUT=$PWD/out RUN=$PWD/out/run IMG=localhost/ao-spike-worker NAME=ao-spike-w
ALLOW_REGISTRIES=proxy.golang.org,sum.golang.org,registry.npmjs.org
TOKEN_FILE=${AGENT_OFFICE_OAUTH_TOKEN_FILE:-$HOME/.config/agent-office/oauth-token}
CLAUDE_REAL=$(readlink -f "$(command -v claude)")
mkdir -p "$OUT" && install -d -m 0700 "$RUN"

build() {
  CGO_ENABLED=0 go build -trimpath -o "$OUT/spike" .
  (cd ../.. && CGO_ENABLED=0 go build -trimpath -o "$OUT/agent-office" ./cmd/agent-office)
  podman build -q -t "$IMG" . >/dev/null
  echo "built: image $IMG, claude -> $CLAUDE_REAL"
}

HOST_PID=
start_host() { # args: host flags
  "$OUT/spike" host -run "$RUN" "$@" >"$OUT/host.log" 2>&1 &
  HOST_PID=$!
  for _ in $(seq 50); do [ -S "$RUN/egress.sock" ] && return; sleep 0.1; done
  echo "host did not start"; cat "$OUT/host.log"; exit 1
}
stop_host() { [ -n "$HOST_PID" ] && kill "$HOST_PID" 2>/dev/null; wait "$HOST_PID" 2>/dev/null || true; HOST_PID=; }
trap 'stop_host; podman rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

hook_settings() { # Claude Code settings: every event -> agent-office hook
  python3 - "$1" <<'PY'
import json, sys
ev = ["SessionStart","UserPromptSubmit","PreToolUse","PostToolUse","PostToolUseFailure",
      "PermissionRequest","Notification","Stop","SubagentStop","SessionEnd"]
h = {e: [{"hooks": [{"type": "command", "command": "/opt/ao/agent-office hook", "timeout": 10}]}] for e in ev}
json.dump({"hooks": h}, open(sys.argv[1], "w"), indent=1)
PY
}

worker() { # args: extra podman flags ... -- command
  local extra=()
  while [ "$1" != "--" ]; do extra+=("$1"); shift; done; shift
  mkdir -p "$OUT/ws" "$OUT/config"
  hook_settings "$OUT/settings.json"
  ${WRAP:-} podman run --rm --init --name "$NAME" --pull=never \
    --network none --userns=keep-id \
    --read-only --cap-drop=all --security-opt no-new-privileges \
    --pids-limit 512 --memory 4g \
    -v "$OUT/ws:/workspace:rw" -w /workspace \
    -v "$OUT/config:/config:rw" \
    -v "$RUN/agent.sock:/run/ao/agent.sock" -v "$RUN/egress.sock:/run/ao/egress.sock" \
    -v "$OUT/spike:/opt/ao/spike:ro" -v "$OUT/agent-office:/opt/ao/agent-office:ro" \
    -v "$OUT/settings.json:/opt/ao/settings.json:ro" \
    -v "$CLAUDE_REAL:/opt/claude/claude:ro" \
    -e HOME=/tmp/home -e CLAUDE_CONFIG_DIR=/config -e DISABLE_AUTOUPDATER=1 \
    -e HTTPS_PROXY=http://127.0.0.1:3128 -e https_proxy=http://127.0.0.1:3128 \
    -e AGENT_OFFICE_SOCKET=/run/ao/agent.sock \
    -e AO_EGRESS_SOCK=/run/ao/egress.sock -e AO_AGENT_SOCK=/run/ao/agent.sock -e AO_API_SOCK=/run/ao/api.sock \
    "${extra[@]}" "$IMG" /opt/ao/spike init -- "$@"
}

case "${1:-}" in
build) build ;;
probe)
  start_host -allow "$ALLOW_REGISTRIES,api.anthropic.com"
  worker -- /opt/ao/spike probe
  echo "--- host log"; cat "$OUT/host.log" ;;
version)
  start_host
  worker -- /opt/claude/claude --version
  echo '{"hook_event_name":"SessionStart","session_id":"x"}' >"$OUT/ws/payload.json"
  worker -- sh -c '/opt/ao/agent-office hook < /workspace/payload.json; echo "hook exit=$?"'
  rm -f "$OUT/ws/payload.json"
  echo "--- host log"; cat "$OUT/host.log" ;;
noauth)
  start_host -allow "${ALLOW:-api.anthropic.com}"
  rm -rf "$OUT/config"; worker ${NONESSENTIAL:+-e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1} -- \
    timeout 60 /opt/claude/claude -p --settings /opt/ao/settings.json "Reply with the single word OK." || echo "exit=$?"
  echo "--- host log"; cat "$OUT/host.log" ;;
auth|inject)
  [ -f "$TOKEN_FILE" ] || { echo "no token file at $TOKEN_FILE"; exit 1; }
  if [ "$1" = auth ]; then start_host -allow "${ALLOW:-api.anthropic.com}" -token-file "$TOKEN_FILE"; cred=socket
  else start_host -allow "${ALLOW:-api.anthropic.com}" -token-file "$TOKEN_FILE" -inject; cred=inject; extra=(-v "$RUN/api.sock:/run/ao/api.sock"); fi
  rm -rf "$OUT/config"; worker -e AO_CREDENTIAL=$cred "${extra[@]}" ${NONESSENTIAL:+-e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1} -- \
    timeout 120 /opt/claude/claude -p --model haiku --settings /opt/ao/settings.json "Reply with the single word OK." || echo "exit=$?"
  echo "--- host log"; cat "$OUT/host.log" ;;
interactive)
  [ -f "$TOKEN_FILE" ] || { echo "no token file at $TOKEN_FILE"; exit 1; }
  start_host -allow "${ALLOW:-api.anthropic.com}" -token-file "$TOKEN_FILE"
  rm -rf "$OUT/config" && mkdir -p "$OUT/config"
  [ -n "${PRESEED:-}" ] && install -m 0600 "$PRESEED" "$OUT/config/.claude.json"
  rm -f "$OUT/pty.in" && mkfifo -m 0600 "$OUT/pty.in"
  WRAP="$OUT/spike pty -in $OUT/pty.in -out $OUT/pty.log --" worker -it -e AO_CREDENTIAL=socket \
    ${NONESSENTIAL:+-e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1} -- \
    /opt/claude/claude --model haiku --settings /opt/ao/settings.json --dangerously-skip-permissions || echo "exit=$?"
  echo "--- host log"; cat "$OUT/host.log" ;;
*) sed -n 2,6p "$0"; exit 2 ;;
esac
