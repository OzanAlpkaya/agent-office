# Screenshots

Refreshed at the end of Phase 13 (2026-11-09) so every picture shows the current UI.

| File | What it shows |
|---|---|
| `team-run.gif` | A real run (`make demo`, [docs/demo.md](../demo.md)): the goal typed into the board, the Tech Lead's live terminal for a moment, then the Office with a room for every role: two sandboxed haiku workers walk in through the lobby's front door and work in parallel (their sub-tasks touch different packages, so file ownership lets both run, ADR 0032), each reports at the Tech Lead's whiteboard and walks out, the lead reports the goal; then the board and the Usage tab. 74 seconds of video (the goal was reported after 57), sped up about 2×. |
| `board-team-run.png` | The board at the end of that run: the goal and both sub-tasks in review, with tokens and cost per task and for the team. |
| `usage-team-run.png` | The Usage tab for that run, by task. |
| `office-live-team.png` | A still from that run: both workers report at the Tech Lead's whiteboard, one on each side of the lead's desk; they have already exited ("2 left the office") and finish their report before walking out (ADR 0045). |
| `office.png` | The Office from the mocked daemon of the UI tests (showcase test): a worker reports at the Tech Lead's whiteboard; the architect, with its plan, and two agents waiting on you queue in the lobby under your office's door, whose sign says "3 waiting" like the Approvals badge; QA at the lab bench, a reviewer at the review table, the assistant at reception, idle agents with coffee in the kitchen, a message flying to the Tech Lead; in the workroom, workers and role-pack roles (docs, security) in shirts of their own colour. |
| `office-rooms.png` | Close-up of the rooms in that picture: Planning, Tech Lead, Your office; QA lab, Review room, Kitchen, Lobby. |
| `office-desks.png` | Close-up of the workroom in that picture: poses, monitors, nameplates with role colours, and empty chairs of agents who are away. |
| `office-messages.png` | Messages between agents flying as envelopes (mocked daemon). |
| `office-phone.png` | The Office on a Pixel 7 (mocked daemon): three stacked bands of rooms above the workroom, the queue in the lobby. |
| `office-queue.png` | A reviewer hits a permission prompt and joins the queue at your door: three waiting, three in the queue, "3 waiting" on the sign (mocked daemon). |
| `approvals-question-plan.png` | The Approvals drawer with an architect's question and its plan waiting for approval, each leading to where it is answered (mocked daemon, ADR 0044). |
| `usage.png` | The Usage tab by task with a goal budget and tokens without a price (mocked daemon). |

The mocked ones (Playwright's headless Chromium; `make ui-test` writes fresh ones to
`web/test-results/screens/`) use fictional agents and tasks. The real ones show work on the
playground project only; the stills and frames sampled across the GIF were checked for local paths
and personal data before they were committed (the repository path appears only in a tooltip,
sandboxed terminals show `/workspace`). None contain captured agent data beyond what is visible.

## Phase 20 (`phase-20/`)

From `web/ui-tests/phase20.spec.ts` (mocked daemon, the tour's world; `AO_SHOTS_OUT` sets where they
go). For the office before this phase, see `phase-18/after/office.png` (windows on the review room's
and the QA lab's back walls, which have rooms behind them).

| File | What it shows |
|---|---|
| `office.png` | The default office: the board summary in the office bar (To do, In progress, Review, Done, Blocked; each opens the Board on its column), the same counts on the Tech Lead's whiteboard, and windows only on the first band's outside walls. |
| `office-whiteboard.png` | Close-up of the whiteboard: the counts in each column's colour above the sub-tasks' sticky notes. |
| `office-classic.png`, `office-compact.png`, `office-open-plan.png`, `office-tall.png` | Each layout preset: windows only where no room is behind the wall; none on the workroom's back wall or over a doorway. |
| `phone-office.png` | The office on a Pixel 7: the counts without labels, so the bar stays one line. |
| `project-settings-cap.png` | A project's Max agents of 12 saved under a daemon-wide cap of 8: the effective value, what it means and the link to the daemon-wide setting. |
