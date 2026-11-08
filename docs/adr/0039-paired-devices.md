# 0039. Authentication: paired devices with one-time codes

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

Until Phase 10 the API had no authentication: the threat model relied on loopback, a Host check
(DNS rebinding) and an Origin check for writes, and noted "later an auth token for the API". With
`tailscale serve` (ADR 0038), requests from a phone arrive at the daemon from 127.0.0.1, exactly like
requests from any local process, so exempting loopback would exempt the phone too. The roadmap's
DoD: unauthenticated requests are rejected. agent-office has one user; every device is that user.

## Options considered

1. **A static shared token** (config file or flag, typed into each browser). Pros: simple. Cons: a
   long secret typed on a phone; one secret for every device, so one lost phone means rotating it
   everywhere; nothing to show which devices have access.
2. **Tailscale identity headers only.** Pros: no pairing step remotely. Cons: forgeable by any local
   process (they connect to the same loopback port); nothing for local browsers; tied to Tailscale.
3. **Paired devices:** a short-lived one-time code pairs a browser, which then holds its own random
   token. Pros: short code instead of a long secret, per-device revocation and a device list, works
   the same locally and remotely, no dependency. Cons: a pairing step for each new browser; the
   first code needs a trusted channel.
4. **Passwords or OAuth.** Pros: familiar. Cons: account management for a single-user tool; a
   password is a long-lived secret to store and rate-limit; OAuth needs an identity provider.

## Decision

Option 3, in package `internal/auth` with migration 0011 (`devices`).

- **Pairing code:** 8 characters of Crockford's base32 (40 bits), shown `ABCD-EFGH`; case, dashes,
  spaces and look-alikes (O→0, I/L→1) are normalized. Valid 5 minutes, single use, at most 5 open,
  in memory only (a restart drops them). After 10 wrong codes within 10 minutes every open code is
  dropped and pairing is refused until the window has passed. Codes are compared in constant time
  against every open code.
- **Device:** `POST /api/auth/pair {code, name}` creates a device with a random 256-bit token
  (`aod_` + base64url). Only its SHA-256 is stored, with the name (1–60 printable characters), the
  kind (`browser`, or `token` for scripts), where it was paired (`local` or `tailscale:<login>`),
  created and last-seen times. The token goes to the browser as cookie `ao_session` (HttpOnly,
  SameSite=Strict, Path=/, Secure when the request came over HTTPS) and is never returned in a body,
  logged or kept in clear.
- **Every route needs a device** unless the route table marks it public: only `GET /api/health`
  (which tells unpaired callers just "ok", not the version), `GET /api/auth/session` and `POST
  /api/auth/pair`. Credentials: the cookie, or `Authorization: Bearer` for scripts. Otherwise 401.
  The UI's static files stay public (the build holds no data), so the app can show its pairing
  screen. `TestEveryRouteNeedsADevice` walks the table, so a new route is protected by default.
- **Expiry and use:** a device unused for 30 days ends (checked on use and at start); last-seen is
  written at most once a minute.
- **Lineage** (added after the end-of-phase review: a stolen phone could pair the thief's browser,
  which outlived the phone's revocation): a code remembers the device that created it, and a
  device records the device whose code paired it (`paired_by`; empty for `agent-office pair`).
  Revoking a device revokes every device paired through it, directly or not, and its open codes.
  Idle expiry ends only the idle device. **Sign out all other devices** (`POST
  /api/devices/sign-out-others`) keeps only the caller.
- **Revocation:** `DELETE /api/devices/{id}` and sign-out (`POST /api/auth/logout`, which revokes the
  caller) delete the row; every request runs with a context the manager cancels on revocation, and
  the WebSockets close with code 4401 ("signed out"), after which the UI shows its pairing screen.
- **Where codes come from:** the first one from `agent-office pair` on the daemon's machine. It asks
  the daemon over a control socket, `$XDG_RUNTIME_DIR/agent-office/control-<hash of the data
  dir>.sock` (mode 0600 in the 0700 runtime directory, the trust of the agent sockets; one per data
  dir so a development daemon and the real one do not collide), and prints the code, a link for this
  machine with the code in the URL fragment (never sent to a server; the page removes it from the
  address bar), and the remote URL. A paired browser creates more codes in its Devices panel.
  `agent-office pair --token <name>` prints a bearer token for scripts (the e2e tests use it).
- The daemon logs pairing and sign-out with device ids and how a device was paired (`local` or
  `tailscale`, never the login), and a hint (no code) when no device is paired.

## Consequences

- Every new browser pairs once; `make pair` and the pairing screen's text say how.
- CSRF: cookies are SameSite=Strict and writes keep the Origin check, so another site cannot use a
  paired browser's session; cross-site WebSockets are refused by the same-origin check.
- A stolen phone keeps access until it is signed out from another device or idles for 30 days. The
  token cannot be read by page scripts (HttpOnly).
- Any local process of the user can still get a code from the control socket, like it can reach
  the agent sockets; that is the existing trust boundary, not a new one.
- Accepted after the review, with reasons:
  - The wrong-code counter is global, so anyone who reaches the pairing endpoint can stop pairing
    for 10 minutes (denial of service only; guessing stays infeasible). Counting per login or
    address would not help: both can be forged by a local process.
  - Cookies are not scoped by port, so on loopback the session cookie also reaches other local web
    servers the browser visits on `127.0.0.1`/`localhost`. Those are local processes of the user,
    which can already ask the control socket for a code (the same boundary).
  - The control socket is created under the process umask and then set to 0600; its directory is
    0700 and must be a real directory owned by the user (checked since the review), so the moment
    in between grants nothing. Changing the umask would race with the daemon's other goroutines.
