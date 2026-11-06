#!/usr/bin/env bash
# Spike C: describe the structure of Claude Code session transcripts (JSONL).
# Prints record types, key names and token-usage field names/sums. Never prints
# message content. Usage: ./analyze.sh <transcript.jsonl>...
set -euo pipefail
for f in "$@"; do
  echo "== $(basename "$f") ($(wc -l <"$f") lines)"
  echo "-- record types (type/subtype: count)"
  jq -r '.type + (if .subtype then "/" + .subtype else "" end)' "$f" | sort | uniq -c
  echo "-- top-level keys per type"
  jq -r '.type as $t | keys | "\($t): " + join(",")' "$f" | sort -u
  echo "-- assistant records: lines vs distinct message.id (content blocks may be split across lines)"
  jq -r 'select(.type=="assistant") | .message.id' "$f" | awk '{n++; s[$0]=1} END {printf "lines=%d distinct_ids=%d\n", n, length(s)}'
  echo "-- message.usage keys"
  jq -r 'select(.type=="assistant") | .message.usage | keys | join(",")' "$f" | sort -u
  echo "-- token sums: per line vs deduplicated by message.id"
  jq -s '[.[] | select(.type=="assistant")] as $a
    | def tot(xs): reduce xs[] as $u ({in:0,out:0,cache_w:0,cache_r:0};
        .in += ($u.input_tokens//0) | .out += ($u.output_tokens//0)
        | .cache_w += ($u.cache_creation_input_tokens//0) | .cache_r += ($u.cache_read_input_tokens//0));
      {per_line: tot([$a[].message.usage]),
       dedup_by_message_id: tot([$a | group_by(.message.id)[] | .[-1].message.usage]),
       models: ([$a[].message.model] | unique)}' "$f" -c
done
