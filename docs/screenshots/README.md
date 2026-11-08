# Screenshots

| File | What it shows |
|---|---|
| `team-run.gif` | A real run (Phase 11, `make demo`, [docs/demo.md](../demo.md)): the goal typed into the board, a sandboxed haiku Tech Lead splitting it into two sub-tasks, two sandboxed workers implementing them in parallel, both reporting, the goal moving to review. About 55 seconds of real time, sped up about 2×. |
| `board-team-run.png` | The board at the end of that run: the goal and both sub-tasks in review, with tokens and cost per task and for the team, and the conflict check (`git merge-tree`, ADR 0032) showing that the two branches conflict with each other. |
| `usage-team-run.png` | The Usage tab for that run, by task. |
| `office.png` | The Office tab with ten agents in every state, from the mocked daemon of the UI tests (Phase 7): a lead planning (whiteboard notes), workers typing, two waiting for the user (raised hands, one for an access request shown in the approval queue above), a reviewer, an idle agent, one starting and one that has exited. |
| `office-desks.png` | Close-up of the desks: poses, monitors, bubbles and nameplates (mocked daemon). |
| `office-messages.png` | Messages between agents flying as envelopes (mocked daemon). |
| `office-live-team.png` | A real run from Phase 7: a haiku lead and two sandboxed workers (`--pack go`). |
| `usage.png` | The Usage tab after one live sandboxed session (Phase 8). |

The mocked ones (Playwright's headless Chromium; `make ui-test` writes fresh ones to
`web/test-results/screens/`) use fictional agents and tasks. The real ones show work on the
playground project only; the stills and frames sampled across the GIF were checked for local paths
and personal data before they were committed. None contain captured agent data beyond what is visible.
