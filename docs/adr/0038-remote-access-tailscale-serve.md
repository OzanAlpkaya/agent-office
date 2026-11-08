# 0038. Remote access: the loopback daemon behind `tailscale serve`

- **Status:** Accepted
- **Date:** 2026-11-08

## Context

Phase 10 lets the user reach agent-office from a phone or another machine. The daemon listens on
loopback only (ADR 0002) and refuses any other `--addr`. The roadmap says: access over Tailscale only,
and the daemon must not be reachable from anywhere else. An installable PWA, service workers and the
clipboard API all need a secure context (`https://` or loopback), so the remote origin must be HTTPS.

Facts verified on 2026-11-08 from Tailscale's docs (kb/1242, kb/1312, kb/1223, the serve CLI
reference) and `ipn/ipnlocal/serve.go`: `tailscale serve --bg 7680` proxies HTTPS on the machine's
MagicDNS name (`<machine>.<tailnet>.ts.net`, certificate managed by Tailscale) to
`http://127.0.0.1:7680` without root. The backend gets the client's Host header and `X-Forwarded-*`;
for tailnet users it adds `Tailscale-User-Login`, `-Name` and `-Profile-Pic`, and strips
client-supplied copies; tagged devices get no identity headers; Funnel (public internet, off unless
enabled with `tailscale funnel`) marks requests with `Tailscale-Funnel-Request: ?1` and sends no
identity. The proxy is Go's `httputil.ReverseProxy`, which passes WebSocket upgrades.

## Options considered

1. **`tailscale serve` in front of the loopback daemon.** Pros: HTTPS with real certificates and no
   TLS code in agent-office; the daemon's only listener stays on loopback; no dependency. Cons: the
   user runs one Tailscale command; requests arrive from 127.0.0.1, so the source address says
   nothing (authentication must cover every request, ADR 0039).
2. **Embed Tailscale (`tsnet`).** Pros: one process, `WhoIs` identity for each connection, no
   separate setup. Cons: a very large dependency tree in a single static binary, its own node
   identity, auth key and state directory to manage; the daemon becomes a network service.
3. **Bind to the tailnet address (100.x / fd7a:…).** Pros: no proxy. Cons: plain HTTP over WireGuard
   is not a secure context, so no installable PWA or clipboard; fixing that means certificates in the
   daemon (`tailscale cert` files to read and renew). The address is also not stable across
   reinstalls.
4. **Any reverse proxy the user likes (Caddy, nginx).** Pros: flexible. Cons: nothing then limits
   access to the tailnet; one typo exposes the daemon to the internet.

## Decision

Option 1.

- The daemon still listens on loopback only; `--addr` keeps refusing other addresses.
- `--remote-host <machine>.<tailnet>.ts.net` (`AGENT_OFFICE_REMOTE_HOSTS`, comma-separated) names the
  Host headers `tailscale serve` forwards. Each must be a DNS name under `.ts.net` with at least
  machine and tailnet labels (no scheme, port, path or IP), so option 4 cannot be configured by
  mistake.
- The guard accepts a remote Host only without a port or with 443. Such a request must carry
  `Tailscale-User-Login` (a tailnet user's device) and must not carry `Tailscale-Funnel-Request`;
  otherwise 403. With `--remote-users you@github,…` (`AGENT_OFFICE_REMOTE_USERS`, added after the
  end-of-phase review) only those logins pass; without it every user of the tailnet reaches the
  pairing screen (and still needs a code). A state-changing request or WebSocket from a remote Host needs Origin
  `https://<that host>`.
- Loopback Hosts keep working with the daemon's port (and the Vite dev origin with `--dev`).
- `docs/remote.md` is the user's guide: Tailscale on both devices, `tailscale serve --bg 7680`,
  `--remote-host`, pairing (ADR 0039), installing the app (ADR 0040), never `tailscale funnel`.

## Consequences

- The daemon cannot tell a remote request from a local one by address. That is why ADR 0039
  authenticates every request; the identity and Funnel headers are defence in depth (a local process
  could forge them, but still needs a device token).
- Tailscale's behaviour is an external contract, documented only in part (the Host and Funnel header
  come from the source). `TestBehindTailscaleServe` runs the API behind a proxy set up the same way;
  a live check on a real tailnet confirms it once (Ozan's phone).
- Users who enable Funnel for this port get 403 for every request instead of a public daemon.
- On a tailnet shared with other people, set `--remote-users`; `docs/remote.md` recommends it.
  Tailscale ACLs that keep others from reaching this machine at all are the stronger layer.
