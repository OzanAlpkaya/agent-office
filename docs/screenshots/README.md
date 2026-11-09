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
