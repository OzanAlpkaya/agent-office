# 0056. One design system, a rail and a tab bar, Settings as a page, cards with a modal

- **Status:** Accepted
- **Date:** 2026-11-12
- **Amends:** ADR 0040 (phone layout), ADR 0044 (the tab strip and the drawers)

## Context

After seventeen phases the UI had grown by addition. Ozan, using it daily: it feels cramped. One
`styles.css` with four colour variables and sizes chosen where they were used; a header with eleven
badges and buttons; Inbox, Memory, Devices, Projects and the pack panel opening as sections under
the header that pushed the board down; task cards carrying every detail and action (a goal card
was often taller than the screen); agent cards in the sidebar with branch, model, imports, events,
messages and six buttons each. On a phone the same pieces stacked.

The request (Phase 18): one design system used by every screen, panels moved to a Settings page,
a clean navigation with the Approvals and Inbox badges always visible, a phone layout for the
installed PWA, compact cards with a detail modal. The office keeps its pixel art.

## Options considered

**Design system**

1. **A component library** (Radix, MUI, Mantine). Pros: tested widgets. Cons: a large dependency
   for a UI with a handful of widget kinds; its look to override; theming through its API.
2. **Utility CSS** (Tailwind). Pros: consistent spacing by construction. Cons: a build step and a
   class vocabulary in every component; the existing markup would be rewritten class by class.
3. **Tokens as CSS custom properties plus a small set of component classes**, base element styles
   using the tokens. Pros: no dependency; the existing markup mostly keeps its classes; a plain
   `<button>` is already the secondary button. Cons: discipline instead of enforcement.

**Navigation**

1. **Keep the tab strip** (Board, Office, Usage, agent tabs) and only restyle. Cons: the strip
   mixed views and terminals; nothing for Settings.
2. **A left rail** with the views and the agents, a top bar for the project and what waits on the
   user; on a phone a bottom tab bar. Agent terminals keep their tabs above the terminal only.

**Settings**: a page with tabs, or a side drawer. A drawer is good for one list (Approvals, Inbox)
but the settings are forms with sections (the project's settings, its office layout, branch
clean-up, every project, the sandbox and grants, memory, devices) that want the full width.

## Decision

Option 3 for the design system (`web/src/theme.css`, `docs/design-system.md`): a 4 px spacing
scale, a type scale of 12 to 24 px, three radii, a neutral dark palette with one accent and
semantic tones, the system font stack (the UI works offline), 32 px controls and 44 px touch
targets on a coarse pointer. Components: buttons (default, `primary`, `ghost`, `danger`, `link`,
`sm`, `icon-button`), chips and status chips by tone, cards and section cards, fields, segmented
controls, tabs, banners and callouts, the modal and the drawer. `styles.css` lays out screens with
these tokens only. Icons are inline SVG (`icons.tsx`), nothing is fetched.

Navigation option 2. The top bar holds the brand, the project switcher with the connection dot,
the sandbox pill (to Settings → Sandbox), the agent finder (`/`), Approvals and Inbox. The rail
holds Board, Office, Usage, Settings, then the agents as compact rows (state, name, role, task,
cost; "needs you" when waiting); exited ones folded. An agent's details and actions (Stop,
Resume, Import, Save work, Remove, Discard; branch, model, imports, events, messages) sit in a
header above its terminal. The open-terminal tabs (ADR 0044) show above a terminal only. On a
phone (≤ 760 px) a bottom tab bar has Board, Office, Agents, Usage and Settings; the agent header
has a back button.

Settings is a page with tabs: Project (the shown project's settings form, its office layout and
branch clean-up, GitHub), Projects, Sandbox (podman only), Memory, Devices. Inbox became a drawer
like Approvals (ADR 0044), with Briefing in its head. Daemon warnings stay as one-line banners;
an API error is a dismissible banner.

A board card shows only its title, one status chip, the agent working on it (and how many more)
and the cost; goals have an accent edge. `cardStatus` (pure, Vitest) picks the chip; the first rule
that applies wins: a plan to approve or something else waiting on the user, a goal's merge (merged,
pushed) or QA (running, failed, passed), what its agents do (needs permission, working, starting),
then the task's status (blocked, done, reported, waiting on n sub-tasks, idle, no agent, to do).
Clicking a card opens its modal (a full-screen sheet on a phone) with the description, acceptance
criteria, plan, messages, QA, Merge and Push, branch clean-up of a done goal, the Tech Lead's
report, agents, sub-tasks, pull requests and cost, and every action in its footer. "Review plan"
opens the goal's modal. New task is a modal. The archive is a segmented mode of the board.

## Consequences

- One place decides spacing and colour; a new screen uses the tokens and component classes.
- Nothing opens under the header any more: panels are drawers or the Settings page, so the board
  never moves (tested).
- Agent actions moved from the sidebar card to the agent's header: one more click to Stop an
  agent you are not looking at, in exchange for a list that shows twenty agents on one screen.
- The office's nameplates and labels keep their own look; a shared `button:active` transform was
  dropped because it moved the nameplates (which are centred with a transform) under a finger.
- Every Playwright spec was updated for the new structure; tests still find things by role and
  text.
