# Demo

Two things: how the README's GIF is recorded, and a short script for showing agent-office live.

## Recording the GIF

`make demo` records a real team run. It is not a test and not part of `make check`: it starts real
Claude Code sessions (haiku, about two cents per run).

```sh
make worker-image PACK=go        # once: the demo's workers use the go pack
make demo AO_DEMO_REPO=~/projects/agent-office-playground AO_DEMO_OUT=/tmp/ao-demo
```

What it does (`web/demo/record-team-run.ts`, run by Node with Playwright's Chromium):

1. Clones `AO_DEMO_REPO` into a throwaway folder named `linkshelf` (the project name the UI shows),
   and starts `bin/agent-office serve` on a free port. It uses a scratch `--data-dir`, never the
   real one, with `--sandbox podman --pack go --model haiku --qa=false --max-agents 3`.
2. Pairs the browser the way a user would: `agent-office pair` prints a `#pair=` link, and the
   script opens it and clicks Pair. A script token (`pair --token`) is used only to poll for the
   end.
3. Records 1280×800 video while it adds the goal on the board, clicks **Start Tech Lead** and
   switches to the Office tab. It waits until the goal is in review and the Tech Lead is idle
   (20-minute limit), keeps the Office on screen for eight more seconds (the Tech Lead reports at
   its whiteboard, the last workers walk out; ADR 0045), then shows the board and the Usage tab.
4. Stops the agents and the daemon, removes the containers, the clone and the data dir
   (`AO_DEMO_KEEP=1` keeps them), and writes `team-run.webm`, PNG stills and `markers.json`
   (seconds into the video at each step) to `AO_DEMO_OUT`.
5. `web/demo/to-gif.sh` speeds the video up to about 35 seconds, then scales it to 800 px at 6 fps
   with 64 colours, which is 4 to 6 MB.

The stills are `office-goal-reported.png`, `board-goal-reported.png` and `usage-team-run.png`,
plus `office-team-working.png` when two workers work at the same time and
`office-worker-reporting.png` when the first worker reports. The goal names the files of each
sub-task (`internal/httpapi` for the health check, `internal/bookmark` for duplicate URLs), so the
Tech Lead declares disjoint files and both workers run at once; sub-tasks that share a file are
run one after the other (ADR 0032), and then the first of those two stills is missing.

Before committing a new GIF or still, look at the stills and at frames across the GIF
(`ffmpeg -ss <s> -i team-run.gif -frames:v 1 f.png`). Check for local paths, names and anything
from your own machine. The real repository path appears only in a tooltip, and sandboxed terminals
show `/workspace`.

Inputs: `AO_DEMO_REPO` (required), `AO_DEMO_OUT` (required for `make demo`), `AO_DEMO_MODEL`
(haiku), `AO_DEMO_PACK` (go), `AO_DEMO_TIMEOUT_MIN` (20), `AO_DEMO_WORK` (parent of the scratch
folder, default the system temp dir), `AO_DEMO_KEEP`.

## Showing it live (about five minutes)

Before the call: build the go pack image, start the daemon on a throwaway clone of the playground
(`make serve REPO=<clone> ARGS="--pack go --model haiku --qa=false"`), pair the browser, and keep
the README's GIF open as a fallback in case the network or the API is slow.

1. **The problem (30 s).** Coding agents are most useful when they can run commands without asking
   every time, and that is the risky part. agent-office lets several of them do that, inside a
   sandbox, while you keep the decisions.
2. **Give the team a goal (1 min).** On the board, add "Health check and duplicate URLs" (the two
   playground backlog items, one in `internal/httpapi` and one in `internal/bookmark`, so they can
   run in parallel; the full text is in `web/demo/record-team-run.ts`) and click **Start Tech Lead**. Open the **Office** tab. The lead's
   whiteboard fills with sub-tasks, workers walk in through the kitchen's front door to their
   desks and start typing, and each one walks to the Tech Lead's whiteboard when it reports, then
   leaves. The idle lead takes a coffee meanwhile (ADR 0045).
3. **Look inside (1 min).** Open a worker's terminal tab: it is a real Claude Code session, live.
   Point at the agent card's "sandbox events": Claude Code tried to reach `downloads.claude.ai`, and
   the egress allowlist refused it. The worker has no network of its own and no credential. Its
   token is a placeholder, and the daemon's API proxy swaps in the real one (ADR 0016).
4. **What came back (1 min).** On the board, the sub-tasks are in review with their cost. The branch
   each worker committed was handed back as a git bundle, verified and imported (ADR 0014). If the
   two branches touched the same lines, the card shows a conflict, found with `git merge-tree`
   before anyone merges (ADR 0032). Open the **Usage** tab for tokens and cost by task.
5. **Where the decisions are (1 min).** The README's design-decisions table and
   [docs/adr/](adr/README.md). Then [docs/plans/](plans/), to show how each phase was planned,
   approved and recorded.

Likely questions, and where the answers are:

- *Why not Claude Code's own sandbox?* ADR 0007, which compares the two.
- *What stops an agent from pushing or merging?* ADR 0014 and ADR 0020, plus the threat model in
  [architecture.md](architecture.md).
- *What does a run cost?* The Usage tab, and ADR 0026 on how it is metered.
- *Can it use other agents than Claude Code?* The adapter layer is built for that. Codex is
  researched in [docs/research/codex-cli.md](research/codex-cli.md) but not implemented.
