# Phase 18 plan: UI and office polish

- **Status:** Done; waiting for Ozan's review. Written 2026-11-12 with the recommended option for every choice, as Ozan asked
  ("run it like the earlier phases ... with the recommended options, no check-ins, one summary at the end").
- **Roadmap:** [docs/plan.md](../plan.md), Phase 18. Progress: [PROGRESS.md](../../PROGRESS.md).
- **Source:** Ozan's use of agent-office after Phase 17.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

The UI is calm and roomy on a desktop and on a phone: one design system, a clean navigation, settings in
one place, cards that show what matters and a modal with everything else. The office can be laid out per
project, shows the user at their desk while they have the UI open, and a done goal can be closed, merged
and cleaned up from the same few dialogs.

## What exists and what it means

- **Styling** is one `styles.css` (about 440 lines) with four colour variables and ad-hoc sizes; buttons,
  badges and chips are styled where they are used.
- **The header** holds the project switcher, version, sandbox and pack badges, GitHub, Approvals, Inbox,
  Memory, Devices and Briefing. Inbox, Memory, Devices, Projects and the pack panel open as sections under
  the header and push the page down; Approvals is already a drawer (ADR 0044).
- **The main pane** has a tab strip (Board, Office, Usage, open agent terminals, the agent switcher); the
  sidebar lists agent cards with every detail and action. On a phone (≤ 760 px, ADR 0040) the sidebar is
  an "Agents" tab.
- **A task card** shows everything: description, plan, QA, inbox notes, agents, usage, PRs, Merge and
  Push, the status select, the role picker and every start button.
- **The office floor plan** (ADR 0045) is computed from the desk grid: two bands of rooms over the
  workroom (wide), or three (stacked, three columns, phones). Rooms, sizes and the number of desks are not
  configurable; desks grow with the agents.
- **The user's desk** in their office is always empty (ADR 0045).
- **The hub's event stream** (`GET /api/events`) is one WebSocket per open page; the server ignores what
  the page sends.
- **After a Merge** the card shows Push; marking the goal done is a separate status change.
- **Branches**: removing an agent deletes its branch only when it has no commits (ADR 0010); a done
  goal's sub-task and QA branches stay forever. A squash merge does not make them ancestors of the target,
  so `git branch --merged` does not see them as merged.

## Decisions (recommended options)

| # | Question | Decision |
|---|---|---|
| 1 | Design direction | Dark, calm and quiet: a near-black neutral background, one raised surface for panels and cards, hairline borders, one accent (a soft blue) and semantic colours for states (green ok, amber needs you, red failed, violet QA). System font stack (the daemon serves the UI offline; no web fonts). Pixel art stays in the office only. |
| 2 | Design system | Tokens as CSS custom properties in `web/src/theme.css`: a 4 px spacing scale (`--space-1`…`--space-8`), a type scale (12, 13, 14, 16, 20, 24 px), radii (6, 10, 14), elevations, colours, focus ring, touch size. Components as classes used by every screen: buttons (`primary`, default secondary, `ghost`, `danger`, `link`; `sm`), chips and status chips, cards, fields, segmented controls, tabs, dialogs and drawers, empty states, banners. Base element styles (buttons, inputs, selects) use the tokens, so a plain `<button>` is already the secondary button. Documented in `docs/design-system.md`. |
| 3 | Navigation | Desktop: a top bar (brand, project switcher, agent finder, Approvals, Inbox, connection) and a left rail with Board, Office, Usage and Settings, then the agents of the project. Open agent terminals keep their tabs (ADR 0044) in a strip that appears above the terminal only. Phone: a top bar (project, Approvals, Inbox) and a bottom tab bar (Board, Office, Agents, Usage, Settings). Approvals and Inbox stay visible on every screen and width. |
| 4 | The panels that push the page down | Memory, Devices, Projects and the sandbox pack panel (with the access grants) move to a **Settings page with tabs**: Project (the shown project's settings, office layout, branch clean-up), Projects (list, add, remove, switch), Sandbox, Memory, Devices. Inbox becomes a drawer like Approvals, since it is a badge; Briefing is a button at the top of the Inbox drawer. Daemon warnings and the host-mode warning stay as compact one-line banners. |
| 5 | The agent list | Compact rows: state dot, name, role, task, cost. The open agent's row expands to its details and actions (branch, model, import, events, messages, Stop, Resume, Import, Save work, Remove, Discard), next to its terminal. |
| 6 | Archive | A board mode, chosen with a segmented control at the top of the Board ("Active · Archive (n)") next to the goal picker, on every width. |
| 7 | Compact card | Title, one status chip, the agent working on it (name, and "+n" for more) and the cost. Nothing else; goal cards have an accent edge. Pure `cardStatus()` picks the chip, first match wins: needs you (a plan to approve or a question), merged / pushed, QA running / failed / passed, working, starting, needs permission, blocked, waiting on sub-tasks, reported (review), done, idle (in progress, nobody on it), to do. |
| 8 | Card modal | A dialog (a full-screen sheet on a phone) with: title, status chip and parent goal; description; acceptance criteria, files and dependencies; the plan (goals); QA; agents (open their terminal); the Tech Lead's report (from the goal record, for goals in review or done); inbox notes; pull requests; cost; and every action in a footer: status, role picker and Start agent, Start architect, Start Tech Lead, Open PR, Merge…, Push…, Mark done, Delete. Escape and the backdrop close it; "Review plan" from Approvals opens the goal's modal. |
| 9 | Office layout format | YAML in the project setting `office_layout` (at most 8 KB), version 1: `rooms` with `id`, `x`, `y`, `w`, `h` on a grid of 16 world-pixel tiles, an optional `name`, `desks` on the workroom; `doors` as pairs of room ids. A door is cut in the middle of the two rooms' shared edge. Every room has a back wall at its top, as today. No visual editor. |
| 10 | Rooms | Required: `lobby` (front door and reception), `work` (desks), `lead`, `user`. Optional: `planning`, `qa`, `review`, `kitchen`. Without a planning room or QA lab, the architect or QA sits at a workroom desk; without a review room reviewers stay at their desk; without a kitchen there is no coffee break. |
| 11 | Validation | In Go (`internal/office`), the only parser: version 1; known, unique room ids; the required rooms; whole tiles inside a 64 × 48 grid; each room at least its furniture's size; no overlaps; 1–32 desks that fit the workroom; doors between existing rooms whose shared edge is at least 3 tiles; every room reachable from the lobby through doors; the user's office has a door. Every problem is reported at once, with where it is. The daemon returns the parsed layout as JSON; the UI never parses YAML. |
| 12 | Invalid layouts | Saving one is refused with every problem (the preview shows them first). A stored layout that does not validate when the project loads (rules of a newer daemon, a hand-edited database) does not stop the project: the office uses the default plan and shows a warning with the problems. |
| 13 | Default and presets | No `office_layout` means today's adaptive plan (wide or stacked, desks grow with the team). Presets, embedded YAML: classic (today's wide plan, 8 desks), compact (4 rooms and a kitchen, 4 desks), open plan (every room around a large workroom, 16 desks), tall (one column, for phones, 6 desks). The project settings pick a preset, edit its YAML and show a preview drawn from the same floor plan code as the office, updated as you type (validated by the daemon). |
| 14 | More workers than desks | With a custom layout, agents beyond its desks get no desk: they are not drawn, and the office bar shows "n without a desk" (each opens the agent). A desk that frees up goes to the agent that has waited longest. The preview warns when the desks are fewer than the project's max agents. The default plan keeps growing. |
| 15 | The user at their desk | Each page reports on its hub WebSocket whether it is visible (on connect and on `visibilitychange`). The hub keeps the connected pages with their device's name and publishes `presence` (the distinct names of devices with a visible page) when it changes. The office seats the user's character (its own look, "You" on the nameplate, the devices in its tooltip) at the user's desk while that list is not empty; the desk is empty when it is. A closed or hidden page stops counting at once; a lost connection when the socket closes. |
| 16 | Merge, then Done | After a successful Merge the same dialog says what was merged and offers **Mark goal done** (with the running-agents warning of Phase 17 when it applies) and Push…; nothing is marked done automatically. |
| 17 | Which branches count as merged | Three classes, for `agent-office/*` branches only: **merged** (its head is an ancestor of the target branch), **squashed** (its head is an ancestor of a recorded goal merge's source whose commit is in the target, or merging it into the target changes nothing: `git merge-tree` gives the target's own tree), **unmerged** (anything else). A branch is **in use**, and never deleted, while it is checked out in a worktree or its agent is not exited. |
| 18 | Goal clean-up | When a goal is done (its modal, the archive record, and the Merge dialog's "Mark goal done"), "Clean up branches…" lists the branches of every agent of the goal and its sub-tasks (workers, QA, lead, ...) that still exist, with their class. Merged and squashed ones are selected; unmerged ones are listed apart, not selected, and deleting any of them needs a second, explicit confirmation naming them. |
| 19 | One-time project clean-up | Settings → Project → Branch clean-up lists every `agent-office/*` branch of the project's repository that is not in use, by class, with the same rules and confirmations. |
| 20 | Deleting | `git update-ref -d refs/heads/<name> <head>`: a compare-and-swap on the head the preview showed (refused if the branch moved), only under `agent-office/`. Each deletion is recorded in `deleted_branches` (name, head, class, goal, when); the archive record lists a goal's deleted branches with `git branch <name> <sha>` to restore one. Any paired device may clean up (the user acting); every deletion is logged. |
| 21 | Screenshots | A Playwright tour (`screens.spec.ts`) against the mocked daemon renders the board, the card modal (before: the full card), settings (before: the panels), the office, the archive and the phone width; the before set is taken from `main` before any change, the after set at the end, into `docs/screenshots/phase-18/`. |

## Sub-phases

1. Plan, roadmap, and the before screenshots (the tour spec run on the current UI).
2. Design system: `theme.css`, the component classes, `docs/design-system.md`.
3. Navigation and Settings: top bar, rail, phone tab bar, Settings page with tabs, Inbox drawer, compact
   agent rows.
4. Compact cards and the modal; Merge then Done.
5. Office layouts: `internal/office` (parse, validate, presets), the project setting, API, the floor plan
   from a layout, missing rooms, deskless agents, the fallback warning, the settings editor and preview.
6. Presence: hub WebSocket messages, the presence update, the user's character.
7. Branch clean-up: migration 0017, git, engine, API, UI.
8. Tests brought up to date, the after screenshots, ADRs, docs, PROGRESS, the full gates.

## Risks

- **The UI change touches every screen and most Playwright tests.** Tests keep finding things by role
  and text; selectors that were layout-specific move with the layout.
- **A general floor plan could break walking**: paths are computed from walls, doorways and furniture
  (ADR 0045), so the layout builder is tested for reachability of every spot from the entrance, per preset.
- **Deleting branches loses work if the classification is wrong**: compare-and-swap on the shown head,
  unmerged only after a second confirmation, heads recorded for restoring, `agent-office/` only.
- **Presence must not leak**: it carries device names (already visible to paired devices in Settings →
  Devices) and nothing else; it is sent only to paired devices.

## Answers to open questions

Every choice took the recommended option, as Ozan asked; they are the decisions above.

## Changes during the phase

- **Agent details moved off the list.** Decision 5 put an agent's details and actions in its row,
  expanded when open. They went into a header above the agent's terminal instead (on every width):
  the rail stays a list of one-line rows, and the phone's terminal view gets the same header with a
  back button. The rows say "needs you" when the agent waits on the user.
- **The open-terminal tabs** show above a terminal only, as planned; on a phone there are none (the
  header names the open agent).
- **Archive** stayed a segmented mode of the board on every width (decision 6); there is no rail entry.
- **Layout parsing stops at room problems.** A room that is missing or misplaced made every door and
  reachability check fail too; problems with rooms are reported first, doors and reachability once
  the rooms are right.
- **Presets:** classic has 12 desks (six columns fit its 26 tiles), open plan 20; the plan said 8 and 16.
- **The user's character is drawn still** (ADR 0058): the user is present whenever the office is
  looked at, and Phase 7's rule that an office where nothing moves draws nothing (render on demand,
  tested) would otherwise never hold. Its desk is a desk node like an agent's, so the empty desk has
  its screen off.
- **Deskless agents and "waited longest":** the order beyond the desks is the order they got their
  place in the assignment, which is arrival order; a freed desk goes to the lowest.
- **Branch clean-up of a goal** lists the branches of every agent of the goal (workers, QA, lead,
  architect, reviewers) that still exist; a goal's lead branch with no commits is usually already gone
  (removal deletes empty branches, ADR 0010).
- **Found by the browser tests:** the shared `button:active` transform overrode the nameplates'
  centring transform, so a tap moved the plate from under the finger and nothing opened; dropped. The
  connection dot was a `status` region and clashed with the terminal's notices; it is an image now.
  Phone touch targets are 44 px (the plan's token), not 40.
- **Independent reviews.** A security review found an integer overflow in the layout's bounds check
  (huge coordinates wrapped past it; values are now checked one by one), a race that could leave a
  page present after it disconnected (the reader now ends before the page is dropped) and branches
  hidden by a tag of the same name (`for-each-ref` reads full refs); fixed. Recorded, not changed: an
  agent started on a branch between a clean-up's preview and its delete is not detected (agent branches
  carry the agent's id, so a new agent never reuses one; a resume re-imports its own); a paired device
  can toggle presence quickly (paired devices are trusted). A code review found the queue at the user's
  door built in the wrong direction for doors between stacked rooms (the doorway's shape was used, not
  the rooms'; a test now checks that the queue starts at the door, and failed for two presets),
  presence updates that could publish out of order (now under the pages' lock), the same presence race,
  and a full scene rebuild on every project update with a custom layout (plans are compared, not
  objects); fixed.

## Review changes (2026-11-12)

Ozan's review before the merge, done on the same branch:

1. **Settings split.** The gear by the project picker opens the shown project's settings, titled with
   its name (General with branch clean-up, Office, Sandbox, Memory); Settings in the rail is the app's
   (Projects, Devices, Profile). Decision: branch clean-up and GitHub stay in the project's General tab
   (they are the project's).
2. **"+ New project"** in the picker opens the add form in a dialog.
3. **Avatar:** Settings → Profile with a name for the nameplate, a live preview drawn with the office's
   art, Randomize and Save; daemon-wide (migration 0018, `/api/profile`), used on every floor; a default
   look and "You" until saved. Parts: skin tone, hair colour, hair style, shirt (every shirt colour the
   office has) and trousers. The user's desk shows them; it is still drawn without animation.
4. **The modal's actions by state** (`detailActions`): nothing greyed out.
5. **Office on a phone:** names only, the task on a tap, the agent on a second tap; no band above or
   below the office (it starts at the top and the workroom's floor fills the rest).
6. **Office on a desktop:** it fills the width at a whole-pixel scale (3 CSS pixels in a 1440 px
   window) and scrolls when taller than the pane.
7. **Settings hints** show the inherited value in full under each field.
8. **Settings tabs on a phone** fit; a strip that overflows fades at its edge.

Found while doing it: a dialog took the focus from a field that focused itself (the add form's
repository path); fixed. The project list's per-project settings form went (a project's Settings opens
its own page). Filling a phone's height with desk rows was tried first and gave rows of empty desks;
the floor is deepened instead.

## Second review change (2026-11-12)

The office at 3x did not fit the pane's height. It now fits the pane in both width and height by
default (**Fit**: the largest sharp scale with the whole floor visible, no scrolling), and **Zoom +**
in the office bar draws it about a third larger for details (it scrolls); the choice is remembered per
device. The step is a third rather than one CSS pixel: on a phone one CSS pixel more (3 device pixels)
made the office wider than the screen. Found while doing it: a pane too narrow for every plan at one
device pixel let a plan wider than the pane be picked; and on a phone a name-only plate still showed
the role, which made plates overlap. The phone screenshots are now taken at a real phone's pixel
ratio (iPhone 14, 3×); the before set was taken at 1×.

