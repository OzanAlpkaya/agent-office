# Screenshots

Taken in Phase 7 with Playwright's headless Chromium (`make ui-test` writes fresh ones to
`web/test-results/screens/`).

| File | What it shows |
|---|---|
| `office.png` | The Office tab with ten agents in every state, from the mocked daemon of the UI tests: a lead planning (whiteboard notes), workers typing, two waiting for the user (raised hands, one for an access request shown in the approval queue above), a reviewer, an idle agent, one starting and one that has exited. |
| `office-desks.png` | Close-up of the desks: poses, monitors, bubbles and nameplates. |
| `office-messages.png` | Messages between agents flying as envelopes (mocked daemon). |
| `office-live-team.png` | A real run: a haiku lead and two sandboxed workers (`--pack go`) on a throwaway clone of the playground. |

The mocked ones use fictional agents and tasks; none contain captured agent data.
