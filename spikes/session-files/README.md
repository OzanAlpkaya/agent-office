# Spike C: Claude Code session files

Throwaway. Question: where do session transcripts live, and where is token usage / cost?

```sh
./analyze.sh <transcript.jsonl>...   # record types, key names, token sums; never content
```

Analyzed the transcripts of the Spike B sessions (Claude Code 2.1.319, haiku). The list of paths
is in `out/` (gitignored).

## Findings

- **Location:** `~/.claude/projects/<cwd with non-alphanumerics replaced by '-'>/<session-id>.jsonl`.
  Subagents get their own files at `<session-id>/subagents/agent-<id>.jsonl`. No path computation is
  needed: every hook payload contains `transcript_path` (and SubagentStop contains
  `agent_transcript_path`). `CLAUDE_CONFIG_DIR` relocates the whole tree.
- **Format is internal and undocumented.** About 20 record types in a 2-turn session, for example
  `user`, `assistant`, `attachment`, `system/stop_hook_summary`, `system/turn_duration`,
  `cost-state`, `tools-used`, `permission-mode`, `ai-title`, `api-request`, `file-history-*`. The
  parser must ignore unknown types and missing fields.
- **Token usage:** each `assistant` record has `message.usage` with `input_tokens`,
  `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens` (plus `cache_creation`
  breakdown, `service_tier`, `server_tool_use`, ...), and `message.model`. Here, lines equalled
  distinct `message.id`s. Longer sessions may split one message across lines, so deduplicate by
  `message.id` anyway.
- **Cost:** a `cost-state` record holds `totalCostUSD` and `modelUsage{<model>: {inputTokens,
  outputTokens, cacheReadInputTokens, cacheCreationInputTokens, costUSD, ...}}`. Its total matched
  the CLI's own `total_cost_usd` exactly (0.00374115 USD for the print run). This is the best source
  for Phase 8, with `message.usage` × a price table as the fallback. Open: whether it includes
  subagent costs.
- **Sensitive content:** `user`/`last-prompt` hold prompts, `api-request-blob` holds full request
  bodies, `session-owner` holds account/organization UUIDs. The Phase 8 parser should pull out
  numeric fields only and never store or forward raw lines.
