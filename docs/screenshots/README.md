# Screenshots

Refreshed at the end of Phase 13 (2026-11-09) so every picture shows the current UI.

| File | What it shows |
|---|---|
| `team-run.gif` | A real run (`make demo`, [docs/demo.md](../demo.md)): the goal typed into the board, the Tech Lead's live terminal for a moment, then the Office: two sandboxed haiku workers walk in through the kitchen door and work in parallel (their sub-tasks touch different packages, so file ownership lets both run, ADR 0032), each reports at the Tech Lead's whiteboard and walks out, the lead reports the goal; then the board and the Usage tab. 66 seconds of video (the goal was reported after 50), sped up about 2×. |
| `board-team-run.png` | The board at the end of that run: the goal and both sub-tasks in review, with tokens and cost per task and for the team. |
| `usage-team-run.png` | The Usage tab for that run, by task. |
| `office-live-team.png` | A still from that run (`office-worker-reporting.png` of `make demo`): one worker reports at the Tech Lead's whiteboard while the other keeps typing at its desk, and its own chair stands empty (ADR 0045). |
| `office.png` | The Office from the mocked daemon of the UI tests (Phase 13 showcase test): a worker reporting at the whiteboard, a reviewer at the review room's table, three idle agents with coffee in the kitchen, a new agent walking in, a message flying to the Tech Lead, and the workroom with agents typing, waiting for the user (raised hands) and starting. |
| `office-rooms.png` | Close-up of the rooms in that picture: the Tech Lead's room, the review room and the kitchen. |
| `office-desks.png` | Close-up of the workroom in that picture: poses, monitors, bubbles, nameplates and empty chairs of agents who are away. |
| `office-messages.png` | Messages between agents flying as envelopes (mocked daemon). |
| `office-phone.png` | The Office on a Pixel 7 (mocked daemon): the stacked floor plan, rooms above the workroom. |
| `usage.png` | The Usage tab by task with a goal budget and tokens without a price (mocked daemon). |

The mocked ones (Playwright's headless Chromium; `make ui-test` writes fresh ones to
`web/test-results/screens/`) use fictional agents and tasks. The real ones show work on the
playground project only; the stills and frames sampled across the GIF were checked for local paths
and personal data before they were committed (the repository path appears only in a tooltip,
sandboxed terminals show `/workspace`). None contain captured agent data beyond what is visible.
