#!/usr/bin/env bash
# Spike E (Claude Code built-in sandbox): run cc-probe.sh through the Bash tool
# with sandbox enabled and an allowlist of example.com. Uses haiku.
set -euo pipefail
cd "$(dirname "$0")"
OUT=out/cc && rm -rf "$OUT" && mkdir -p "$OUT/ws"
cp cc-probe.sh "$OUT/ws/probe.sh"

SETTINGS='{"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,"network":{"allowedDomains":["example.com"]}}}'

# Same env scrubbing as the daemon will do (see spikes/hooks).
CLEAN_ENV=$(env | cut -d= -f1 | grep -E '^(CLAUDECODE|CLAUDE_PID|CLAUDE_EFFORT|CLAUDE_CODE_)' | sed 's/^/-u /' | tr '\n' ' ')

(cd "$OUT/ws" && echo "Run exactly this command with the Bash tool: bash probe.sh — then reply with only the word done." \
  | env $CLEAN_ENV claude -p --model haiku --settings "$SETTINGS" \
      --output-format stream-json --verbose --allowedTools "Bash(bash probe.sh)" > ../stream.jsonl 2> ../stderr.log) || true

echo "== probe output (from the tool result)"
jq -r 'select(.type=="user") | .message.content[]? | select(.type=="tool_result") | (.content | if type=="array" then map(.text // "") | join("") else . end)' "$OUT/stream.jsonl"
echo "== tool_use_result sandbox flags"
jq -c 'select(.type=="user" and .tool_use_result != null) | {sandboxed: (.tool_use_result.sandboxed // .sandboxed // null), interrupted: .tool_use_result.interrupted}' "$OUT/stream.jsonl" || true
echo "== result"
jq -c 'select(.type=="result") | {subtype, is_error, total_cost_usd}' "$OUT/stream.jsonl"
echo "== host side"
ls "$OUT" "$OUT/ws"
head -c 400 "$OUT/stderr.log"
