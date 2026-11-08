# 0040. PWA and the phone UI: a hand-written service worker, a phone layout, terminal keys

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

The DoD: the PWA installs on a phone and shows the board, terminals and approvals. The UI (ADR 0005)
was laid out for a desktop: a 300 px agent sidebar, a five-column board, an xterm.js terminal that
sends every key to the agent. A phone has no Esc, Tab or arrow keys, which Claude Code needs (Esc
interrupts a turn, ADR 0008; arrows and Enter answer its menus). Ozan's backlog from Phase 7: copy
and paste in the terminal (Ctrl+C must keep interrupting; Ctrl+Shift+C opened Chrome's devtools; a
selection was not copied).

## Options considered

**Service worker.**
1. **vite-plugin-pwa / Workbox** with precaching. Pros: offline app shell. Cons: a new dependency;
   a cached UI can outlive a daemon upgrade and talk to a newer API; caching data is wrong for a
   live board.
2. **Hand-written, offline page only.** Pros: about 30 lines, no dependency, nothing stale. Cons:
   no offline use (there is nothing useful to do offline anyway: the agents run on the daemon).

**Two viewers of one terminal** (a phone and a desktop) share one PTY size.
1. Last resize wins (today): the desktop resizes the PTY back whenever its window changes.
2. **The terminal that gets focus sets the size**, like tmux's "latest" window size.
3. Per-viewer emulation in the daemon (a headless terminal per viewer). Heavy (ADR 0002).

**Ctrl+Shift+V.** Read the clipboard API (a permission prompt) or let the browser's own paste event
reach xterm.js (no prompt).

## Decision

- **PWA:** `manifest.webmanifest` (standalone, theme colours, `start_url` `/`), icons 192 and 512 px,
  a maskable 512 px (art inside the 80 % safe zone, tested) and a 180 px Apple touch icon, drawn in
  code by `web/icons` (golden-file test; original art like ADR 0024). A hand-written `sw.js` caches
  only `offline.html` and an icon, answers page loads network-first with the offline page as the
  fallback, and never touches `/api/`. The daemon serves the page, manifest and worker with
  `Cache-Control: no-cache` and Vite's hashed assets as immutable. `index.html` has the manifest,
  theme colour, Apple meta tags and `viewport-fit=cover`.
- **Phone layout** (below 760 px): the agent sidebar becomes an **Agents** tab; board columns snap
  sideways one per screen; header badges wrap; approvals and panels stay at the top and scroll
  inside a capped height; touch targets are at least 44 px; the page itself never scrolls (tested
  on Pixel 7 and iPhone 14 viewports).
- **Terminal keys:** on touch or narrow screens a key bar sends Esc, Tab, Shift+Tab, the arrows
  (following the terminal's cursor-key mode), Ctrl-C and Enter, and Paste reads the clipboard into
  xterm's paste path (bracketed paste). Buttons keep the focus in the terminal.
- **The focused terminal sets the PTY size** (option 2): it sends its size on focus.
- **Copy and paste:** an xterm.js custom key handler: Ctrl+Shift+C copies the selection and calls
  `preventDefault`, so the browser does not open devtools; Ctrl+Shift+V is not sent to the agent and
  the browser's paste event reaches xterm (no permission prompt); a selection is copied when the
  mouse button is released (anywhere, since a drag often ends outside the terminal); Ctrl+C without
  Shift goes to the agent as before. A right-click menu on live and saved terminals has Copy and
  Paste, so copying never depends on a browser shortcut. F12 and Ctrl+Shift+I/J are left to the
  browser.
- **How text is copied** (changed after Ozan's manual test, where copying silently did nothing in
  Chrome): the first version called only `navigator.clipboard.writeText`, which a browser refuses
  when the page lacks the clipboard-write permission (`NotAllowedError: Write permission denied`,
  reproduced), and it dropped the error. Copies now go first through the browser's copy event inside
  the user's gesture (`document.execCommand("copy")` filling `clipboardData`, no permission needed;
  deprecated but the only permission-free way), then through the async API; a failure shows "Copy
  failed: <reason>" over the terminal. Paste from the menu or the key bar reads the clipboard (the
  browser may ask once); its failure says that Ctrl+Shift+V needs no permission.
- **Claude Code's fullscreen mode and OSC 52** (Ozan's second manual test: a highlighted selection in
  a resumed agent's terminal gave "Copy failed: nothing is selected"). His sessions (sandboxed,
  with Claude Code's clean config) run in fullscreen mode: alternate screen and mouse tracking
  (1000/1002/1003/1006), so Claude Code takes the mouse, draws its own selection and sends it as an
  OSC 52 clipboard write; xterm.js then has no selection, and without a handler it dropped the OSC
  52. The terminal now handles OSC 52, under rules, since a clipboard write from the agent is a
  clipboard-injection risk (it could replace what the user is about to paste into a shell):
  accepted only within 3 s of the user's own mouse or key gesture in that terminal, never from the
  replayed scrollback (the first message) or a saved session, never a read request (`?`), payloads
  capped at 1 MiB of base64; anything refused shows a notice. Ctrl+Shift+C says when Claude Code has
  the mouse; Shift+drag still makes an xterm.js selection there. Checked live with a sandboxed
  Claude Code 2.1.319 session (no clipboard-write permission).
- **Content-Security-Policy** (from the end-of-phase review): `default-src 'self'; script-src
  'self'` without eval, inline styles allowed (React style props, xterm.js), images `'self' data:
  blob:`, workers `'self' blob:`, connections to `'self'` plus this daemon's own `ws://`/`wss://`
  origin, `object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. PixiJS
  generates shader code with `Function` unless `pixi.js/unsafe-eval` (part of the pixi.js package)
  is loaded, so the office loads it. Checked live against the real daemon: office, terminals and the
  phone layout without a violation.
- Tests run Chromium only (ADR 0025): phone viewports, `Page.getInstallabilityErrors` over CDP, the
  offline page, clipboard round trips with only clipboard-read granted (to read back), so the
  refused-write case of the manual test is the one tested, plus a test of the failure notice. The other UI tests block service
  workers, since a worker would bypass the mocked daemon's `page.route`.

## Consequences

- No offline use; the offline page says what to check (daemon, Tailscale, serve).
- Whether a page can keep Ctrl+Shift+C from devtools is the browser's decision. agent-office calls
  `preventDefault`, which should stop it, but tests cannot press browser shortcuts: not verified, a
  manual check for Ozan.
- Touch selection in xterm.js is limited on phones; copying from a phone terminal is not a goal.
- Copy on selection (asked for in the backlog) puts whatever is selected on the system clipboard,
  secrets an agent printed included; the guide says so.
- The Playwright tests run against `vite preview`, which sends no CSP; the policy is covered by
  `TestSecurityHeaders` and the live check.
- Push notifications (an approval waiting) need Web Push with a push service and VAPID keys; a
  later phase can add them.
