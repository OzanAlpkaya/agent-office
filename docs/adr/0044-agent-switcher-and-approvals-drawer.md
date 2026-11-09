# 0044. Agent switcher, open tabs and the approvals drawer

- **Status:** Accepted; amended 2026-11-09 (what counts as waiting; crowded tabs; see the end)
- **Date:** 2026-11-09

## Context

From Ozan's own use, two problems with many agents:

- Every agent with a terminal had a tab in the tab strip. At five or six agents the strip
  overflowed; with 20 it was unusable, and on a phone it was a long sideways scroll.
- Access requests (ADR 0019) appeared as a section at the top of the page, above everything. Each
  new request pushed the page down, sometimes under the user's pointer, and on a phone it took
  half the screen.

The UI has to stay usable with 20 agents and on a phone (ADR 0040), and pending approvals have
to stay easy to notice, since agents wait on them.

## Options considered

Agents:

1. **Group the tabs by goal or role.** Still one tab per agent; it does not reach 20.
2. **An overflow menu ("+12").** Keeps the strip tidy but hides agents behind an unordered list.
3. **A tree in the sidebar only.** No sidebar on a phone (ADR 0040).
4. **Tabs only for the agents the user opened, plus a searchable switcher for all of them.** The
   pattern of editors and browsers: a few working tabs, and a quick way to reach anything else.

Approvals:

1. **A toast.** Goes away on its own, so it is easy to miss; covers part of the page while shown.
2. **A fixed section on the board.** Only visible on one tab.
3. **A header badge that opens a drawer.** The badge is always in the same place, so its arrival
   moves nothing; the drawer lies over the page.

## Decision

Agents: option 4.

- The tab strip has Board, Office and Usage, then the agents the user opened: from a card, the
  Office, the board, the inbox, the switcher, or by starting an agent. At most six. Opening a
  seventh closes the least recently used one. A tab closes with its × or a middle-click, and
  moves by dragging or Alt+←/→. The open tabs and their order are kept in the browser's
  `localStorage`, as a per-viewer convenience; blocked storage only means starting with none.
  When an agent is removed, its tab closes.
- The **switcher** opens from the "Agents" button at the end of the strip, or with `/` when the
  focus is not in a text field or a terminal. Typing filters by name, id, role, task, goal and
  branch (every word must match). Agents that have not exited are grouped by goal: the
  coordinating roles come first, then workers in start order, then agents without a task.
  Exited agents come last, most recent first. The arrow keys and Enter choose an agent. On a
  phone the switcher is a full-screen sheet, and the strip shows only the open agent.
- The sidebar lists running agents first, and exited ones in a collapsed "Exited" group.

Approvals: option 3. The "Approvals" badge is always in the header and shows a count while
requests wait. It opens a drawer on the right (a bottom sheet on a phone) with the queue as
before: the agent's reason as text, and Allow or Deny. The drawer does not open on its own,
because covering the page uninvited is the problem being fixed. Instead:

- the badge turns violet and pulses three times (not with reduced motion);
- the browser tab's title starts with the count;
- the waiting agent's card links to the drawer;
- the switcher marks the agent as "approval waiting";
- the Office still shows a raised hand.

## Consequences

- With 20 agents the strip holds at most six tabs, and any agent is a few keystrokes away; the
  Playwright tests cover 20 agents on a desktop and on a phone.
- Arriving requests move nothing: a Playwright test compares the board's and the header's
  positions before and after a request arrives, and with the drawer open.
- A request is one click further away than before (badge, then Allow). The count, the title and
  the card link make up for it.
- The Inbox, Memory and Devices panels still open inline under the header, which is fine
  because the user opens them; they could move into the same drawer later.

## Amendment (2026-11-09): one list of what waits on the user

The first version counted pending access requests in the badge, the drawer and the tab title,
while the Office's "waiting for you" counted agents whose pose was waiting: at a terminal
permission prompt (host mode), or with a pending request. An unsandboxed agent at a permission
prompt therefore showed in the Office but not in the badge. And an agent with two requests counted
twice in the badge, once in the Office. With the queue out of the page, the badge is the main
signal, so it must include everything.

- `waitingOnUser` (`web/src/waiting.ts`) is the one definition: the agents, not requests, that
  are not exited and have pending access requests, a permission prompt, or both. Exited agents
  are left out: the daemon expires their requests when they exit.
- These all read that list: the badge, the tab title, the drawer, the card link, the switcher's
  "approval waiting", and the Office pose. The pose decides the "waiting for you" count, so the
  numbers cannot drift apart. A Playwright test with both kinds of waiting agents checks every
  count, also after live changes.
- The drawer has one entry per agent. Requests keep Allow and Deny. A permission prompt can only
  be answered in the agent's terminal, so its entry says so and offers "Go to terminal", which
  opens the agent's tab.

Also amended:

- **Crowded tabs:** with more than three agent tabs, inactive tabs show only their name, with
  the task in the tooltip. The open tab does not shrink (up to 18rem), so its task stays
  readable. Shrinking every tab alike had cut the open tab's task to one letter.
- **First paint:** the `/` shortcut and the switcher's focus are set up in layout effects,
  before the first paint. Under CPU throttling, a `/` pressed right after the page appeared was
  lost, and so could the first keys typed into the switcher.

