#!/bin/bash
# Spike G runner (throwaway). Output goes to out/ (gitignored); logs never hold a token.
#   ./run.sh tls                       throwaway CA + loopback server certificate in out/tls
#   ./run.sh tui <home> <log> <steps> -- <codex args>   interactive Codex in a PTY behind the TLS proxy
set -euo pipefail
cd "$(dirname "$0")"
OUT=$PWD/out
DRIVE=${DRIVE:-$PWD/ptydrive.py}
TLSDIR=$OUT/tls
CERT=$TLSDIR/server.pem
PRIV=$TLSDIR/server-priv.pem
CA=$TLSDIR/ca.pem

case "${1:-}" in
tls)
  CGO_ENABLED=0 go build -o "$OUT/spike" .
  "$OUT/spike" tls -dir "$TLSDIR"
  ls "$TLSDIR" ;;
tui)
  home=$2 log=$3 steps=$4; shift 4; [ "$1" = -- ] && shift
  : > "$OUT/proxy.log"
  "$OUT/spike" proxy -listen 127.0.0.1:18443 -tls-cert "$CERT" -tls-priv "$PRIV" -auth "$HOME/.codex/auth.json" -log "$OUT/proxy.log" &
  pp=$!
  sleep 0.4
  (cd "$OUT/ws" && CODEX_CA_CERTIFICATE=$CA CODEX_HOME=$home python3 -I "$DRIVE" "$log" "$steps" -- codex "$@") || true
  kill $pp ;;
*) sed -n 2,4p "$0"; exit 2 ;;
esac
