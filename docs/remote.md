# Remote access: agent-office on your phone

agent-office's daemon listens on loopback only. To use it from your phone or another machine, put
[Tailscale](https://tailscale.com)'s `tailscale serve` in front of it: your tailnet devices reach it
over HTTPS at `https://<machine>.<tailnet>.ts.net/`, and nothing else can (ADR 0038). Every browser,
local or remote, is paired once with a one-time code (ADR 0039). On a phone, agent-office installs as
an app (ADR 0040).

## Setup (once)

1. Install Tailscale on this machine and on the phone, and log in to the same tailnet with both.
   Enable MagicDNS and HTTPS certificates in the tailnet's admin console (DNS page), if they are not
   on already.
2. Find this machine's name on the tailnet:

   ```sh
   tailscale status --json | jq -r .Self.DNSName   # e.g. box.tail1234.ts.net.
   ```

3. Forward HTTPS on that name to the daemon (stays on in the background, survives reboots):

   ```sh
   tailscale serve --bg 7680          # the daemon's --addr port
   tailscale serve status             # shows https://box.tail1234.ts.net -> http://127.0.0.1:7680
   ```

   Never use `tailscale funnel` for agent-office: it publishes the port on the internet.
   agent-office refuses every request that comes through Funnel anyway.

4. Tell the daemon its name (without the trailing dot):

   ```sh
   agent-office serve --repo ~/my/repo --remote-host box.tail1234.ts.net
   # or AGENT_OFFICE_REMOTE_HOSTS=box.tail1234.ts.net
   ```

On a tailnet you share with other people, also name who may come in:

```sh
agent-office serve ... --remote-host box.tail1234.ts.net --remote-users you@github
```

## Pairing

The first browser is paired from a terminal on this machine:

```sh
agent-office pair            # make pair for the dev daemon of `make serve`
# Pairing code: K7QM-2XPA (works once, until 14:05:31)
# This machine: open http://127.0.0.1:7680/#pair=K7QM-2XPA
# Phone or other machine on your tailnet: open https://box.tail1234.ts.net/ and enter the code
```

A code works once and for 5 minutes. A paired browser creates more in **Settings → Devices → Pair a device**:
open the tailnet URL on the phone, type the code and a name for the device. After 10 wrong codes in
10 minutes, pairing stops for a while and the open codes are dropped.

**Settings → Devices** lists every paired browser and script, where it was paired (this machine, or over
Tailscale with your login) and when it was last used. **Sign out device** ends one at once,
including its open terminals, together with every device that was paired with a code it created;
**Sign out all other devices** keeps only the browser you are using. A device unused for 30 days
ends by itself. Sign out a lost phone from any other device.

For scripts, `agent-office pair --token <name>` prints a token to send as
`Authorization: Bearer <token>`; it shows up in Devices like a browser.

## Install the app

- **Android (Chrome):** open the tailnet URL, then the menu → **Install app** (or **Add to Home
  screen**).
- **iPhone (Safari):** open the tailnet URL, **Share → Add to Home Screen**.

The app opens full screen. On a phone the agent list is the **Agents** tab, the board's columns
swipe sideways, and an agent's terminal has a key bar for what the phone keyboard lacks: Esc
(interrupts a turn), Tab, Shift+Tab, the arrows, Ctrl-C, Enter and Paste. When you type in a
terminal, it takes that terminal's size; your desktop takes it back when you click into it there.

When the daemon or Tailscale is down, the app shows an offline page instead of a browser error.

## Copy and paste in the terminal (all browsers)

- Select text to copy it; **Ctrl+Shift+C** copies the selection too. When Claude Code runs in its
  fullscreen mode it handles the mouse and copies its own selection; agent-office passes that to
  your clipboard only right after your selection. Hold **Shift** while dragging for the terminal's
  own selection instead.
- **Right-click** the terminal for a menu with **Copy** and **Paste**.
- **Ctrl+Shift+V** pastes. **Ctrl+C** still interrupts the agent, as in a terminal.
- **F12** opens the browser's devtools, also while the terminal has focus.
- If the browser refuses, a short "Copy failed: …" or "Paste failed: …" notice says why. Paste from
  the menu may ask once for permission to read the clipboard; Ctrl+Shift+V never does.
- Selections go to the system clipboard as they are made, including anything secret the agent
  printed.

## Troubleshooting

| What you see | Why, and what to do |
|---|---|
| `forbidden host` | The name in the address bar is not one of `--remote-host`, or you used a port. Use `https://<name>/` and pass that name to the daemon. |
| "this Tailscale user may not use this agent-office" | Your login is not in `--remote-users`. |
| "accepts remote requests only from a tailnet user's device" | The request came from a tagged device or without Tailscale's identity; use a device logged in as you. |
| "refuses requests through Tailscale Funnel" | Funnel is on for this port; turn it off (`tailscale funnel status` shows it; Tailscale's Funnel docs say how) and use `tailscale serve` only. |
| "no agent-office daemon is running with data dir …" from `pair` | Start the daemon, or pass its `--data-dir` (the dev daemon: `make pair`). |
| The pairing screen comes back | This device was signed out (from Settings → Devices, or after 30 idle days). Pair it again. |
| `tailscale serve` is refused for your user | Tailscale's docs say forwarding a port needs no root, but some installs restrict the CLI; run it with `sudo` or follow Tailscale's docs for your distribution. |
