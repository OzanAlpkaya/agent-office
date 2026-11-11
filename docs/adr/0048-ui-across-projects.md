# 0048. The UI across projects: one shown, the others watched lightly

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

With several projects (ADR 0046) the board, usage, inbox and memory are per project, but the
Approvals badge must count what waits on the user in every project, and the office should show one
floor per project. ADR 0044 made `waitingOnUser` the one source for the badge, drawer, title and
office after they had disagreed; that must hold across projects too.

## Options considered

1. **The daemon counts waits per project** and streams the numbers. Cheap for the UI, but the
   rule would exist twice (Go and TypeScript) and drift, the problem ADR 0044 fixed.
2. **One daemon stream with every project's updates**, and the UI keeps every project's full world.
   One rule, but every project's messages, events and usage would be fetched and held all the time.
3. **The shown project's full world, every other project watched lightly**: one events stream per
   other project, keeping only the four inputs of `waitingOnUser` (agents, access requests, inbox,
   plans).

## Decision

Option 3.

- A `Root` follows the daemon's `/api/events` (the project list), picks the project to show (the
  browser's last choice, else `serve --repo`'s, else the first that is open), and remounts the
  `App` for another one, so nothing of one project's view (tabs, panels, the office's scene) leaks
  into another's. With no project, or one that could not open, it shows the Projects panel.
- The `App` calls only its project's routes, through a per-project client provided by React
  context (`useApi`); there is no unprefixed helper left to call by mistake.
- Every other open project has a light watcher (`reduceLight`, pure and tested); the badge and the
  title count `waitingOnUser` of the shown project plus each other project's, and the drawer lists
  the other projects' waits under their names. Their access requests are decided right there; a
  click on an agent switches to its project and opens it.
- A project switcher in the header names each other project with what runs and waits there; a
  Projects panel adds, changes (empty fields inherit the daemon's flags, shown as placeholders),
  retries and removes projects, with an inline confirmation instead of a browser dialog.
- The office gets an elevator: one floor per project with its waiting count; choosing a floor
  switches the project. Desks are kept per floor for the page's lifetime (ADR 0024).
- Usage shows the shown project as before plus an "All projects" table (totals per project and
  overall) when there is more than one.

## Consequences

- One WebSocket per open project per browser; fine for the handful of projects a local daemon runs.
  A light watcher does not fetch messages, sandbox events or usage.
- With one project the UI looks as before (no elevator, no All projects table, the same title).
- Tested in Vitest (`projects.test.ts`: picking, the project list, the light reducer, the badge
  across projects, the settings form) and Playwright (`projects.spec.ts`: switching, approvals
  across projects, adding and removing, settings, floors, usage, no project yet) against a mocked
  daemon that serves several projects.
