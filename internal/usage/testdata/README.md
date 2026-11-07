# usage test fixtures

Redacted, hand-written fixtures with the record shapes of Claude Code 2.1.319 transcripts and of
the Messages API's responses (checked 2026-11-08 on real data, keys and numbers only). No real
prompts, ids or paths. `SENSITIVE` marks text that must never reach a row, a log or an update.

The same three messages appear in both forms:

| Message | Model | Transcript | API response |
|---|---|---|---|
| `msg_01AAAA` | claude-opus-5-5 | main transcript, split over two lines | `stream_a.sse` (cumulative delta with cache fields) |
| `msg_01BBBB` | claude-haiku-5-5 | main transcript | `message_b.json` (not streamed) |
| `msg_01CCCC` | claude-sonnet-5-5 | subagent transcript | `stream_c.sse` (delta with output only) |

Their cost with the built-in table is 0.053648 + 0.0002 + 0.015016 = 0.068864 USD, which is the
transcript's `cost-state.totalCostUSD`.
