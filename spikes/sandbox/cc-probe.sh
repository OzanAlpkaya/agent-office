#!/usr/bin/env bash
# Run by Claude Code's Bash tool inside its built-in sandbox (see run-cc-sandbox.sh).
# Prints one line per check. Only checks existence, never reads private files.
echo "uid=$(id -u)"
c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 https://example.com); echo "https example.com -> ${c:-fail}"
c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 https://example.org); echo "https example.org -> ${c:-fail}"
c=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 --noproxy '*' https://example.com); echo "https example.com bypassing proxy -> ${c:-fail}"
touch ./inside.txt 2>/dev/null && echo "write cwd -> ok" || echo "write cwd -> blocked"
touch ../outside.txt 2>/dev/null && echo "write outside cwd -> ok" || echo "write outside cwd -> blocked"
cat /etc/hostname >/dev/null 2>&1 && echo "read /etc/hostname -> ok" || echo "read /etc/hostname -> blocked"
test -d "$HOME/.ssh" && echo "home ssh dir visible -> yes" || echo "home ssh dir visible -> no"
[ -n "${HTTPS_PROXY:-}${https_proxy:-}" ] && echo "proxy env set -> yes" || echo "proxy env set -> no"
