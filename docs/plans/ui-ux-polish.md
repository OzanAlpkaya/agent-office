# UI/UX polish pass (before Phase 13)

Requested by Ozan on 2026-11-09, based on issues from his own use. He asked for the recommended
options without questions, so this plan was not approved separately. Branch `ui/ux-polish`.

## Goal

The UI holds up with many agents: you can find and switch between 20 agents on a desktop and on
a phone, an approval request never moves the page, agents have names a person can remember, and
the Office shows only the people who are still at work.

## Issues and recommended options

1. **Terminal tabs do not scale.** Today every agent with a terminal gets a tab.
   - Chosen: the tab strip keeps Board, Office and Usage, and shows only the agents you
     *opened*, as tabs you can close (×, or middle-click) and reorder (drag, or Alt+←/→). It holds
     at most six; opening a seventh closes the one used least recently. An **agent switcher**
     (a button in the tab strip, or the `/` key outside text fields and terminals) lists every
     agent, with a search over name, id, role, task and branch. Running agents are grouped by goal
     and exited ones are listed last. On a phone the switcher is a full-screen sheet, and the strip
     shows only the agent that is open.
   - The sidebar lists running agents first; exited ones are in a collapsed "Exited (n)" group.
   - Open tabs and their order are remembered per browser (localStorage, a per-viewer
     convenience).
   - Alternatives: grouping the tabs themselves by goal (still one tab per agent, so it does not
     reach 20); an overflow menu only (hides the agents instead of letting you find them); a
     sidebar-only tree (does not work on a phone).
2. **Approvals push the page down.**
   - Chosen: an **Approvals** badge in the header, always in the same place, highlighted with the
     count while requests wait. It opens a drawer on the right (a bottom sheet on a phone) that
     lies over the page, so nothing moves. While requests wait, the tab title starts with the
     count, and the waiting agent's card links to the drawer. The drawer does not open by itself:
     that would cover what you are doing.
   - Alternatives: a toast (disappears, and is easy to miss); a fixed section on the board (only
     visible on one tab).
3. **Agents are hex ids.**
   - Chosen: the daemon gives each agent a first name when it starts, from a built-in list of 130
     common names from many languages and regions. The name is stored with the agent (migration
     0012), kept through resumes, follow-ups and restarts, and unique among the agents that are not
     removed. If all 130 are taken, it is unique among running agents, and then it gets a number.
     The id stays the key in the API, logs and branch names; the UI shows the name first and the
     short id as secondary text.
   - Alternatives: names chosen in the browser from a hash of the id (cannot be unique, and would
     differ between devices once the list changes); names chosen by the agents themselves (not
     deterministic, and an agent's text is not trusted).
4. **Exited agents stay at their desks.**
   - Chosen: an exited agent leaves the Office. Its character walks to the door and fades out
     (about 1.5 s; reduced motion: it simply disappears), its nameplate goes, and its desk becomes
     free for the next agent. Exited agents stay in the agent list, the switcher, history and Usage.
     A resumed agent comes back to the lowest free desk. ADR 0024 is amended.

## Files

- Daemon: `internal/store/migrations/0012_agent_names.sql`, `queries.sql` and the generated code,
  `internal/engine/names.go` (+ test), `engine.go` (assign, reserve, restore), API type `name`.
- UI: `api.ts` (`name`, `agentLabel`), `App.tsx` (header badge, drawer, tabs, sidebar groups),
  a new `AgentSwitcher.tsx` and `tabs.ts` (+ unit tests), `Board.tsx`, `Team.tsx`, `Usage.tsx`,
  `TerminalView.tsx` (names where ids were shown), `office/model.ts`, `office/scene.ts`,
  `office/Office.tsx`, `styles.css`.
- Tests: Vitest for the tab logic, the switcher's search and grouping, and the office model;
  Playwright for the switcher (desktop and phone), the approvals drawer (no layout shift), names,
  and an agent leaving the Office. Go tests for name assignment.
- Docs: ADR 0043 (agent names), ADR 0044 (agent switcher and approvals drawer), ADR 0024 amended,
  `docs/architecture.md`, `PROGRESS.md`.

## Risks

- A layout shift that tests do not see: the Playwright test measures the board's position before
  and after an approval arrives.
- The walk-out animation must keep render-on-demand: it is drawn only while someone walks.
- Names in agents' own text: agents still see and use ids (the team tools are unchanged), so an
  inbox message may mention an id. The UI shows both, so it can be matched.

## Changes during the pass

- The name list has 130 names, not 120, written as a mix of names usually given to women, to men
  and to either (not counted as an exact split).
- The tab strip's open tabs shrink their task text before the strip scrolls, and the open tab is
  scrolled into view; on a phone the whole strip scrolls and the switcher button stays at its
  right edge. Both came from looking at the screenshots: with six tabs at 1440 px the open tab was
  off screen, and on a phone the open tab was squeezed to two letters.
- The switcher's count is of running agents, like the "Agents · n" tab on a phone (it first
  counted all agents, so the phone showed 19 and 20 side by side).
- An agent's name link in an approval had the 44 px touch height of the approval buttons, which
  broke the sentence over three lines on a phone; it is inline text now.
- The leaving character gets two leg frames (new pixel maps in `art.ts`, original like the rest)
  and walks in a straight line over the furniture; paths between rooms are Phase 13's.
- The mocked daemon in the UI tests names its agents (fixed names per id) and answers approve and
  deny; `MockDaemon.accessRequest` sends a live request.
- Existing tests changed where behaviour changed on purpose: exited agents are not counted in the
  Office; approvals are opened from the badge; cards are found by `data-agent`, not by id text;
  an exited agent's card is in the collapsed group.
- The new tab test failed once in a full-file run; see the follow-up below for the cause.
- Not done: the README's GIF and screenshots still show hex ids from the Phase 11 recording;
  re-recording needs a real team run (`make demo`, a few cents of tokens).

## Follow-up (2026-11-09, after Ozan's review)

Ozan's review asked for four things, recommended options again, without questions.

1. **Approvals count mismatch.** The cause, checked in the code before changing it:
   - The Office's pose is "waiting" for `state === "waiting_permission"` or a pending access
     request (`office/model.ts`, `office/pose.ts`).
   - The badge, title and drawer counted pending requests only (`App.tsx`). Omar in the tests is
     unsandboxed and at a terminal permission prompt, so the Office said 2 and the badge 1.
   - A second mismatch of units: the badge counted requests and the Office counted agents.

   Fixed with one list (`waiting.ts`, ADR 0044 amended), counted in agents. The drawer has one
   entry per agent, and a permission prompt gets "Go to terminal". A UI test with both kinds
   compares the badge, title, drawer, Office count and raised hands, also after live changes.
   The test fixture no longer has a pending request for an agent that is not in the world (the
   daemon expires those), so the phone test uses `team(8)`, which has a request.
2. **Flaky tabs test.**
   - Reproduced: `--repeat-each=30` failed 1 of 30. Under 4x CPU throttling (`AO_CPU_THROTTLE=4`,
     a Chrome DevTools Protocol setting in the spec's `beforeEach`, off by default) it failed 10 of
     30, always at the check after the reload.
   - Cause, from logging the first read, the stored tabs and the cards: after `page.reload()` the
     test waited for the board, then read the tabs once (`allTextContents`). The board renders
     before the daemon's snapshot arrives, and tabs render only for agents the snapshot names. The
     stored tabs were intact every time, and the tabs appeared moments later. This was a test bug,
     not lost tabs.
   - Fix: retrying assertions (`toHaveText`) wherever the test read the tabs once, with no retries
     or longer timeouts.
   - Running the whole spec throttled showed a real race too: the `/` listener was attached after
     the first paint, so a `/` pressed right away was lost (3 of 10). It is a layout effect now, as
     is the switcher's focus.
   - After the fix: 30 of 30 throttled, 30 of 30 normal, and the whole spec 60 of 60 throttled.
3. **Agents see names** (ADR 0043 extended):
   - each agent's first prompt states its own name;
   - messages, reports to the lead, `list_tasks`, `get_briefing`, `assign_task` and `start_qa` name
     agents as "Yuki (c3d4e5f6a7b8)", with the full id because the tools take it;
   - commit trailers and GitHub comments keep `agent-<id>`.

   Not run: the real-Claude e2e tests, whose prompts changed by one paragraph; the engine tests
   with the fake CLI check the prompt, the messages and the tool outputs.
4. **Readable open tab:** with more than three agent tabs, inactive tabs show only their name,
   and the open tab keeps its task (it does not shrink, up to 18rem). A test checks that the open
   tab's task fits.
