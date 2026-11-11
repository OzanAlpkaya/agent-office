# Design system

The UI's look in one place (Phase 18, ADR 0056). Tokens and shared components live in
[`web/src/theme.css`](../web/src/theme.css); screens lay themselves out in
[`web/src/styles.css`](../web/src/styles.css) using only these tokens. The pixel office keeps its own
palette ([`web/src/office/art.ts`](../web/src/office/art.ts)).

## Direction

Dark, calm and quiet: a near-black neutral background, one raised surface for panels and cards,
hairline borders, one accent (a soft blue) for what is active or primary, and semantic tones for
states. Text carries the information; colour marks a state, never decorates. No web fonts: the
daemon serves the UI offline.

## Tokens

| Group | Tokens |
|---|---|
| Surfaces | `--bg` (page), `--bg-sunken` (terminal), `--surface` (panels, columns), `--surface-2` (cards, controls), `--surface-3` (hover, chips) |
| Lines | `--border`, `--border-strong` |
| Text | `--text`, `--text-2` (secondary), `--muted`, `--faint` |
| Accent | `--accent`, `--accent-strong`, `--accent-bg`, `--on-accent` |
| States | `--ok`, `--warn`, `--danger`, `--violet` (approvals, merges), `--teal` (reported), each with a `-bg` tint |
| Roles | `--role-lead`, `--role-architect`, `--role-qa`, `--role-reviewer`, `--role-assistant` |
| Type | `--font-sans`, `--font-mono`; sizes `--text-xs` 12, `--text-sm` 13, `--text-base` 14, `--text-md` 16, `--text-lg` 20, `--text-xl` 24 |
| Space | `--space-1` 4 … `--space-8` 48 (4, 8, 12, 16, 20, 24, 32, 48) |
| Shape | `--radius-sm` 6, `--radius` 10, `--radius-lg` 14, `--radius-pill`; `--shadow-1`, `--shadow-pop`; `--focus` |
| Controls | `--control-h` 32, `--control-h-sm` 26, `--touch` 44 (phones and coarse pointers) |
| Shell | `--topbar-h`, `--rail-w`, `--tabbar-h` |

## Components

| Component | Markup |
|---|---|
| Button | `<button>` is the secondary button; `.primary` (one per area), `.ghost` (toolbars, rows), `.danger` (destructive, after a confirmation), `.link`; `.sm`, `.icon-button`, `.block` |
| Chip | `.chip`; a state as `.chip.status-chip.tone-{ok,warn,danger,accent,violet,teal,muted}` (a dot and a tinted fill); `.count` for numbers |
| Field | `<label class="setting"><span>Label</span><input/select/textarea></label>`; `.settings-grid` lays several out |
| Card | `.section-card` (a titled section with an optional `.lede`); `.surface` for a plain panel |
| Segmented | `.segmented` with buttons carrying `aria-pressed` |
| Tabs | `.tab-list` with `role="tab"` buttons and `aria-selected` |
| Banner, callout | `.banner.warn` / `.banner.danger` (full width, one line); `.callout.warn` / `.callout.danger` inside content |
| Dialog | `Modal` (centred, a full-screen sheet on a phone) and `Drawer` (right edge, a bottom sheet on a phone): both lie over the page, close on Escape and the backdrop, and return focus |
| Empty state | `.empty` |
| Icons | `Icon` from `icons.tsx`: 20 × 20 line icons in `currentColor`, decorative (the control has the name) |

## Rules

- Spacing, sizes, colours and radii come from tokens; no raw values in screen CSS except for the
  office's overlays, which match the pixel art.
- Nothing opens under the header and pushes the page: use a drawer, a modal or the Settings page.
- Every interactive element has an accessible name; counts in a name are spelled out
  ("Approvals: 2 waiting").
- On a phone (≤ 760 px or a coarse pointer) controls are at least 44 px high and nothing scrolls
  sideways but the board's columns and the office.
- Reduced motion turns off the UI's own animations.
