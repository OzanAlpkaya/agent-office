#!/usr/bin/env bash
# Spike E (Podman): worker on an internal network, egress only via allowlist proxy.
# Builds a FROM-scratch image locally (no registry pulls). Cleans up after itself.
set -euo pipefail
cd "$(dirname "$0")"
P=ao-spike
OUT=out/podman && rm -rf "$OUT" && mkdir -p "$OUT/workspace"

cleanup() {
  podman rm -f "$P-proxy" "$P-inspect" >/dev/null 2>&1 || true
  podman network rm "$P-internal" >/dev/null 2>&1 || true
  podman secret rm "$P-secret" >/dev/null 2>&1 || true
  podman rmi -f "localhost/$P-egress" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

CGO_ENABLED=0 go build -trimpath -ldflags=-s -o "$OUT/egress" .
printf 'FROM scratch\nCOPY egress /egress\nENTRYPOINT ["/egress"]\n' > "$OUT/Containerfile"
podman build -q -t "localhost/$P-egress" "$OUT" >/dev/null

# Internal network: no route out and no DNS at all (no DNS exfiltration channel).
# Workers reach the proxy by a static IP.
SUBNET=10.90.77.0/24 PROXY_IP=10.90.77.2
podman network create --internal --disable-dns --subnet "$SUBNET" "$P-internal" >/dev/null
printf 'dummy-value-not-a-secret' | podman secret create "$P-secret" - >/dev/null

# Proxy: on the default network (internet + DNS) and the internal network (workers).
# Note: with DNS enabled on the internal network, its resolver answered first and
# refused external names, even for the proxy; --dns did not override it.
podman run -d --pull=never --name "$P-proxy" --network podman --network "$P-internal:ip=$PROXY_IP" \
  --read-only --cap-drop=all --security-opt no-new-privileges \
  "localhost/$P-egress" proxy -listen :3128 -allow example.com >/dev/null
sleep 1

WORKER_FLAGS=(
  --network "$P-internal"
  --userns=keep-id
  --read-only --cap-drop=all --security-opt no-new-privileges
  --pids-limit 128 --memory 256m
  -v "$PWD/$OUT/workspace:/workspace"
  -v /etc/ssl/certs/ca-certificates.crt:/etc/ssl/certs/ca-certificates.crt:ro
  -e HTTPS_PROXY="http://$PROXY_IP:3128"
  --secret "$P-secret,type=env,target=TEST_SECRET"
)

echo "== worker probe (internal network, proxy allowlist: example.com)"
podman run --rm --pull=never "${WORKER_FLAGS[@]}" "localhost/$P-egress" probe

echo "== host view of the workspace"
ls -ln "$OUT/workspace" | tail -n +2 | awk '{print "owner uid=" $3 " file=" $9}'
echo "host uid=$(id -u)"

echo "== proxy decisions"
podman logs "$P-proxy" 2>&1 | grep -E "allow|deny|upstream" | sed -E 's/^time=[^ ]+ //'

echo "== does 'podman inspect' expose env values? (secret vs plain -e)"
podman create --pull=never --name "$P-inspect" -e PLAIN_ENV=dummy-plain-value \
  --secret "$P-secret,type=env,target=TEST_SECRET" "localhost/$P-egress" probe >/dev/null
ENVS=$(podman inspect "$P-inspect" --format '{{.Config.Env}}')
echo "plain -e value visible in inspect:   $(grep -c dummy-plain-value <<<"$ENVS" || true)"
echo "podman secret value visible in inspect: $(grep -c dummy-value-not-a-secret <<<"$ENVS" || true)"

echo "== container with --network none reaches a host unix socket via bind mount"
SOCK="$PWD/$OUT/agent.sock"
"$OUT/egress" serve-unix "$SOCK" & SERVER=$!
sleep 0.5
podman run --rm --pull=never --network none --userns=keep-id --read-only --cap-drop=all \
  --security-opt no-new-privileges -v "$SOCK:/run/agent-office.sock" \
  "localhost/$P-egress" dial-unix /run/agent-office.sock
wait $SERVER
