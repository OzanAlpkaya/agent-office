# Phase 10 plan: Remote access (PWA over Tailscale)

- **Status:** In progress on `phase-10/remote-access`. Written 2026-11-08 with the recommended option
  for every open question and executed straight away, as Ozan asked ("Write the Phase 10 plan to
  docs/plans/phase-10.md using your recommended options, then execute it immediately without asking
  me anything"). Every decision is listed in the end-of-phase summary; nothing is merged until Ozan
  has read it.
- **Roadmap:** [docs/plan.md](../plan.md), Phase 10. Progress: [PROGRESS.md](../../PROGRESS.md).
  Phase order (Ozan, 2026-11-08): 8, 12, 9, 10, 11, 13.
- **Prerequisites:** none for building and testing. The live check on a phone needs Tailscale on this
  machine and on the phone (installing it needs sudo), so that last step is Ozan's; everything up to
  it is tested here against a TLS reverse proxy that behaves like `tailscale serve`.

This is the plan as written, followed by the answers to its open questions and the changes made
during the phase.

## Goal

Use agent-office from a phone or another machine of the user's tailnet: install it as an app, see
the board, the terminals and the approvals, and act on them, while the daemon stays unreachable
from anywhere else and every request proves it comes from a device the user paired.

## Definition of Done (from docs/plan.md, refined)

- [ ] The PWA installs on a phone and shows the board, terminals and approvals: a valid manifest,
      icons and a service worker (Chromium reports no installability errors, tested); at phone width
      the board, an agent's terminal (with a key bar for Esc, Tab, arrows, Ctrl-C, Enter and paste)
      and the approval queue are usable without horizontal page scrolling (Playwright with phone
      viewports). Live check on Ozan's phone over Tailscale.
- [ ] The daemon is not reachable outside localhost/Tailscale: it still listens on loopback only
      (`--addr` refuses anything else); remote access goes through `tailscale serve`, and the daemon
      accepts a remote Host only for the configured `*.ts.net` names, with Tailscale's identity
      header and without its Funnel header (tested through a proxy that mimics `tailscale serve`).
- [ ] Unauthenticated requests are rejected: every API route and both WebSockets answer 401 without
      a paired device's credential (a test walks the route table, so a new route is covered by
      default); the only public endpoints are the UI's static files, health, the session check and
      pairing. A revoked device is refused at once and its open WebSockets are closed.
- [ ] Backlog: copy and paste in the browser terminal: Ctrl+C still interrupts, Ctrl+Shift+C copies
      the selection (and does not open devtools), Ctrl+Shift+V pastes, a mouse selection is copied;
      a Paste button on phones.

## What exists and what it means

- The daemon binds `--addr` (default `127.0.0.1:7680`) and refuses non-loopback addresses
  ("remote access is a later phase"). Its guard accepts only loopback Host headers with its port
  (DNS rebinding) and, for state-changing requests, a same-host or `--dev` Origin; WebSockets keep
  coder/websocket's same-origin check. There is no authentication: the threat model's first row
  says "later an auth token for the API".
- `tailscale serve` (verified in Tailscale's docs and `ipn/ipnlocal/serve.go`, 2026-11-08): `tailscale
  serve --bg 7680` proxies HTTPS on the machine's MagicDNS name (`<machine>.<tailnet>.ts.net`, a
  Let's Encrypt certificate Tailscale manages) to `127.0.0.1:7680`, without root. The backend gets the
  client's Host (`<machine>.<tailnet>.ts.net`), `X-Forwarded-*`, and for tailnet users
  `Tailscale-User-Login` / `-Name` / `-Profile-Pic`; client-supplied copies of these are stripped.
  Tagged devices get no identity headers; Funnel requests (public internet, off unless enabled with
  `tailscale funnel`) carry `Tailscale-Funnel-Request: ?1` and no identity. It is a Go
  `httputil.ReverseProxy`, which passes WebSocket upgrades.
- Through `tailscale serve`, remote requests arrive at the daemon **from 127.0.0.1**: the source
  address cannot tell a phone from a local process, so authentication has to cover every request,
  local ones included.
- The UI is one React page with a 300 px agent sidebar, a five-column board, tabs and terminals, laid
  out for a desktop. xterm.js 6 sends every key to the PTY; there is no copy or paste handling.
- Clipboard and service workers need a secure context: `https://` (tailscale serve) or loopback.

## Out of scope

- Embedding Tailscale (`tsnet`), binding to the tailnet address, or terminating TLS in the daemon
  (see open question 1).
- Accounts, passwords, OAuth, several users with different rights: agent-office stays a
  single-user tool; every paired device is the user.
- Push notifications (Web Push needs a push service reachable from the daemon and VAPID keys); the
  inbox and approval queue show what waits once the app is opened. Recorded as a follow-up.
- Offline use: the service worker shows an offline page, it does not cache the app or data.
- A QR code for pairing (it needs a QR encoder dependency); the code is 8 characters, typed once.

## Approach

1. **Authentication: paired devices** (new package `internal/auth`, migration 0011 `devices`).
   - A **pairing code** is 8 characters of Crockford base32 (40 bits, shown `ABCD-EFGH`; case,
     dashes and the look-alikes O/I/L are normalized), valid 5 minutes, single use, at most 5
     outstanding, kept in memory only. After 10 wrong codes in 10 minutes all outstanding codes are
     dropped and pairing is refused for the rest of the window.
   - **Pairing** (`POST /api/auth/pair {code, name}`) creates a device: a random 256-bit token
     (`aod_…`) whose SHA-256 is stored with the device's name, when it was paired, from where (local
     or the Tailscale login) and when it was last seen. The token goes to the browser as a cookie
     (`ao_session`, HttpOnly, SameSite=Strict, Path=/, Secure over HTTPS) and is never shown, logged
     or stored in clear.
   - **Every request** under `/api/` except `GET /api/health`, `GET /api/auth/session` and `POST
     /api/auth/pair` needs a valid device: the cookie, or `Authorization: Bearer` for scripts.
     Otherwise 401 JSON. A device unused for 30 days expires; last-seen is written at most once a
     minute. The UI's static files stay public (they hold no data), so the app can show its pairing
     screen. Health tells unauthenticated callers only "ok", not the version.
   - **Revocation** (`DELETE /api/devices/{id}`, "Sign out" = revoke this device) deletes the row,
     and the device's open WebSockets are closed with code 4401.
   - **Codes come from** `agent-office pair` (the first device; it asks the running daemon over a
     new user-only control socket `$XDG_RUNTIME_DIR/agent-office/control-<hash of the data dir>.sock`
     and prints the code, a link for this machine with the code in the URL fragment, and the remote
     URL) or from a paired browser's Devices panel ("Pair a device"). `agent-office pair --token
     <name>` prints a bearer token for scripts (the e2e tests use it); it shows up as a device and is
     revoked the same way.
   - Route table: the API's routes move into one table that the auth middleware and a test share, so
     the test asserts 401 for every route that is not explicitly public.
2. **Remote access through `tailscale serve`.**
   - The daemon still listens on loopback only. `--remote-host <machine>.<tailnet>.ts.net`
     (`AGENT_OFFICE_REMOTE_HOSTS`, comma-separated) adds Host names the guard accepts (no port or
     443); each must be a DNS name under `.ts.net`, so a public reverse proxy cannot be configured by
     mistake.
   - A request with a remote Host must carry `Tailscale-User-Login` (only tailnet users' devices
     have it; Funnel and tagged devices do not) and must not carry `Tailscale-Funnel-Request`, else
     403. Defence in depth: a local process could forge these, but it still needs a device token.
   - Origins: a state-changing request or WebSocket from a remote Host needs Origin
     `https://<that host>`.
   - `docs/remote.md`: install Tailscale, `tailscale serve --bg 7680`, `--remote-host`, pairing the
     phone, installing the app, revoking, "never `tailscale funnel`", troubleshooting.
   - The UI shows the remote URL(s) in the Devices panel (`GET /api/status` gains `remote_hosts`).
3. **Security headers on every response:** `Content-Security-Policy: frame-ancestors 'none'` and
   `X-Frame-Options: DENY` (the approval buttons cannot be clickjacked), `X-Content-Type-Options:
   nosniff`, `Referrer-Policy: no-referrer`; `Cache-Control: no-store` on API responses.
4. **PWA.**
   - `manifest.webmanifest` (name, standalone display, theme colours, start URL `/`), icons 192 and
     512 px plus a maskable 512 px and a 180 px Apple touch icon: pixel art drawn in code (a Go
     package renders them; a test checks the committed PNGs match it), original like the office art.
   - A hand-written service worker (`sw.js`, no Workbox): it caches only an offline page and the
     icons, answers navigations network-first with the offline page as fallback, and never touches
     `/api/`. Served with `no-cache` so a new daemon's UI is picked up.
   - `index.html`: manifest link, theme colour, Apple meta tags, `viewport-fit=cover`.
5. **Responsive UI** (below 760 px wide):
   - The agent sidebar becomes an **Agents** tab; the board's columns scroll sideways with snap, one
     column per screen width; header badges wrap; the approval queue and inbox stay at the top with
     44 px touch targets; tables scroll inside their panel. No horizontal page scroll.
   - **Terminal:** the terminal you focus sets the PTY size (it sends its size on focus), so a phone
     and a desktop watching the same agent take turns instead of fighting; a key bar on touch or
     narrow screens sends Esc, Tab, Shift+Tab, arrows, Ctrl-C, Enter, and pastes.
6. **Copy and paste** (xterm's custom key handler and selection API): Ctrl+Shift+C copies the
   selection (and is swallowed so the browser does not open devtools); Ctrl+Shift+V lets the
   browser's paste event through to xterm (bracketed paste, no clipboard permission needed) and is not
   sent as ^V; a selection is copied when it is made; Ctrl+C without Shift is unchanged (interrupt).
7. **Pairing and devices in the UI:** a pairing screen when the session check says not paired or a
   request or WebSocket is refused with 401/4401 (code field, prefilled from `#pair=`, the device name
   prefilled from the browser); a **Devices** panel (paired devices, this one marked, last seen,
   revoke, sign out, "Pair a device" with the code, its expiry and the remote URL).
8. **Tests:** `internal/auth` unit tests (codes, limits, expiry, hashing, revocation closing
   watchers); API tests (every route 401, WebSockets 401, pairing sets the cookie flags, bearer
   tokens, revoked device, remote Host rules, Funnel and identity headers, origins, security
   headers, control socket); an integration test through a TLS `httputil.ReverseProxy` set up like
   `tailscale serve` (pair, cookie, API, events and terminal WebSockets); cmd tests (`--remote-host`
   validation, `pair`); Vitest for the key handler, code normalization and device naming; Playwright:
   pairing screen, Devices panel, phone viewports (board, terminal with key bar, approvals, no
   horizontal scroll), installability (`Page.getInstallabilityErrors` over CDP), copy/paste with
   clipboard permissions. `make e2e` pairs with `agent-office pair --token`.

## Risks

- **Locking Ozan out of his own daemon:** the first browser needs `agent-office pair`. Mitigation:
  the pairing screen says exactly what to run; `make pair` uses the dev data dir; the daemon logs a
  hint (no code) when no device is paired.
- **A stolen device token** (a lost phone): revocable from any other paired device, expires after 30
  idle days; HttpOnly so page scripts cannot read it.
- **Tailscale behaviour changes** (headers, Host): covered by the docs and source read on 2026-11-08
  and the mimicking proxy test; the live check confirms it once.
- **xterm.js on phones** (soft keyboard, selection by touch) is imperfect; the key bar and Paste
  button cover what Claude Code needs (Esc, arrows, Enter, Tab).
- **Ctrl+Shift+C and devtools:** a page can swallow it with `preventDefault`, but Playwright cannot
  press browser shortcuts, so this part is a manual check.

## Steps (each one or more commits with `make check` green)

1. `docs:` this plan.
2. `feat(auth):` package `auth`, migration 0011, store queries.
3. `feat(api):` route table, authentication middleware, pairing and device endpoints, control socket,
   security headers, remote hosts; `pair` command and `--remote-host`.
4. `test(e2e):` the harness pairs with a token.
5. `feat(web):` pairing screen and Devices panel.
6. `feat(web):` PWA (manifest, icons, service worker).
7. `feat(web):` responsive layout and terminal key bar.
8. `feat(web):` terminal copy and paste.
9. `test:` Playwright (phone viewports, installability, pairing, copy/paste); `make ui-test`,
   `make sandbox-test`, `make e2e`.
10. `docs:` ADRs 0038–0040, `docs/remote.md`, architecture and threat model, plan.md, PROGRESS.md,
    README.

## Open questions (answered with the recommended option)

1. **How does the tailnet reach the daemon?** `tailscale serve` in front of the loopback daemon.
   It gives HTTPS (needed for a secure context: PWA install, service worker, clipboard) with
   certificates Tailscale manages, keeps the daemon's listener on loopback, and adds no dependency.
   `tsnet` would embed a whole tailnet node (a large dependency tree, its own auth key and state);
   binding to the 100.x address would be plain HTTP (no secure context, so no installable PWA or
   clipboard) and would need its own TLS to fix that.
2. **How are requests authenticated?** Devices paired once with a short-lived code, then a
   per-device token in an HttpOnly cookie. A static shared token would have to be typed into every
   device and could not be revoked per device; Tailscale's identity headers alone can be forged by
   any local process (requests from tailscale serve also come from loopback) and say nothing for
   local browsers.
3. **Does loopback need authentication too?** Yes: through tailscale serve, remote requests arrive
   from loopback, so an exemption by address would exempt them as well; it also closes the "any
   local process or website can drive the daemon" gap of the threat model's first row.
4. **Where does the first code come from?** `agent-office pair` over a user-only control socket
   (the same trust as the agent sockets), printed in the user's terminal, never in the log.
5. **Restrict remote access to certain Tailscale users?** Not in this phase: pairing already limits
   access to the user's devices; the identity header is required (rejecting Funnel and tagged
   devices) and recorded with the device. An allowlist of logins is easy to add later.
6. **Service worker: Workbox / vite-plugin-pwa or hand-written?** Hand-written, about 40 lines: the
   app must not be cached (a daemon upgrade must not serve a stale UI against a new API), so only an
   offline page is; no new dependency.
7. **PTY size with two viewers?** The terminal that gets focus sets the size (like tmux's
   "latest" window size); no per-viewer emulation in the daemon.

## Changes during the phase

(appended while the phase runs)
