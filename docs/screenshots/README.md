# Screenshots

Refreshed at the end of Phase 13 (2026-11-09) so every picture shows the current UI.

| File | What it shows |
|---|---|
| `team-run.gif` | A real run (`make demo`, [docs/demo.md](../demo.md)): the goal typed into the board, a sandboxed haiku Tech Lead splitting it into two sub-tasks, each sandboxed worker walking in through the kitchen door, implementing its sub-task, reporting at the Tech Lead's whiteboard and walking out, the lead's coffee break and its own report, then the Usage tab. 107 seconds of real time, sped up about 3×; the lead ran the sub-tasks one after the other because both touch the same file (ADR 0032). |
| `board-team-run.png` | The board at the end of that run: the goal and both sub-tasks in review, with tokens and cost per task and for the team, and the merge-tree conflict check (ADR 0032). |
| `usage-team-run.png` | The Usage tab for that run, by task. |
| `office-live-team.png` | A still from that run: a worker reports at the whiteboard next to the Tech Lead while the next worker walks in through the kitchen's front door (ADR 0045). |
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
