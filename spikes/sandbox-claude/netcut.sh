#!/usr/bin/env bash
# Phase 20 spike: cut the host's upstream mid-response, then restore it.
set -u
cd "$(dirname "$0")"
rm -f out/cut out/cutproxy.log
./out/spike cutproxy -addr 127.0.0.1:18080 -ctl out/cut > out/cutproxy.log 2>&1 &
CP=$!
rm -rf out/ws && mkdir -p out/ws && (cd out/ws && git init -q && git -c user.email=a@b -c user.name=s commit -q --allow-empty -m init)
HTTPS_PROXY=http://127.0.0.1:18080 FLAG_SETTINGS=1 PRESEED=out/preseed.json \
  PROMPT='Write a story of about 1500 words about a lighthouse keeper, directly as your answer. Do not use any tools.' \
  timeout 400 ./run.sh interactive > out/interactive.out 2>&1 &
RUN=$!
sleep 2
until grep -q "UserPromptSubmit" out/host.log 2>/dev/null; do sleep 0.5; done
until grep -q "cutproxy: tunnel" out/cutproxy.log; do sleep 0.5; done
sleep ${CUT_AFTER:-6}
touch out/cut; echo "CUT $(date +%T.%N)"
sleep ${DOWN_FOR:-60}
rm -f out/cut; echo "UP $(date +%T.%N)"
sleep ${AFTER:-40}
echo "DONE $(date +%T.%N)"
timeout 3 sh -c "printf '/exit\r' > out/pty.in"
sleep 5; kill $CP 2>/dev/null; wait $RUN 2>/dev/null
