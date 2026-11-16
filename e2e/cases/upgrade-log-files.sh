#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# An install made before #70 writes no log files, so the console's Logs page
# stays empty. inbuxa upgrade says how to turn them on, and does it given an
# API key, whether or not there is anything else to upgrade.
#
# Run from the host with: OLD_REF=<an installer before #70> e2e/vm/run.sh e2e/cases/upgrade-log-files.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
SERVER=http://127.0.0.1:8081
[ -x /tmp/inbuxa-old ] || { echo "needs OLD_REF: an installer before #70"; exit 1; }

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
jmap() { # jmap TOKEN-or-user:pass JSON-CALLS
  local auth
  case "$1" in *:*) auth=(-u "$1") ;; *) auth=(-H "Authorization: Bearer $1") ;; esac
  curl -s "${auth[@]}" -H 'Content-Type: application/json' \
    -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"urn:inbuxa:jmap:registry\"],\"methodCalls\":$2}" "$SERVER/jmap/"
}
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }
logfile() {
  local vol
  vol=$($(rt) volume inspect -f '{{.Mountpoint}}' "$($(rt) volume ls -q | grep 'inbuxa-data$' | head -1)" 2>/dev/null)
  ls -t "$vol"/logs/inbuxa.log.* 2>/dev/null | head -1
}

echo "==> installed by an installer from before #70"
OUT="$(/tmp/inbuxa-old install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed with $(/tmp/inbuxa-old version)" || { bad "old install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
sleep 10
[ -z "$(logfile)" ] && ok "no log files" || bad "log files already: $(logfile)"

echo
echo "==> upgrade without a key says how to turn them on"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "upgrade exit 0" || { bad "upgrade exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
grep -q "inbuxa upgrade --key -" <<<"$OUT" && ok "it says to run it with --key" || { bad "no word of --key"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
[ -z "$(logfile)" ] && ok "and turns nothing on" || bad "log files without a key: $(logfile)"

echo
echo "==> an API key, as an administrator makes one in the console"
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
printf 'services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n' > /tmp/basic.yaml
compose -f /tmp/basic.yaml up -d server >/dev/null 2>&1; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"log files test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
compose up -d server >/dev/null 2>&1; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }

echo
echo "==> upgrade with the key turns them on"
OUT="$(echo "$KEY" | /tmp/inbuxa upgrade --key - --yes 2>&1)"; rc=$?
echo "$OUT" | tail -6 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "upgrade exit 0" || bad "upgrade exit $rc"
grep -q "log files in /var/lib/inbuxa/logs" <<<"$OUT" && ok "it says where they are" || bad "no word of where"
for _ in $(seq 1 30); do [ -s "$(logfile)" ] && break; sleep 2; done
[ -s "$(logfile)" ] && ok "a log file: $(basename "$(logfile)")" || bad "still no log file"
grep -q '"logFiles": true' /etc/inbuxa/install.json && ok "the install record says it has them" || bad "the install record doesn't say logFiles"
TRACERS=$(jmap "$KEY" '[["x:Tracer/query",{},"0"]]' | grep -o '"ids":\[[^]]*\]' | tr -cd ',' | wc -c)
ok "the server has $((TRACERS + 1)) tracer(s)"

echo
echo "==> and once is enough"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "Nothing to do" <<<"$OUT" && ok "nothing to do" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
grep -q -- "--key -" <<<"$OUT" && bad "it still asks for a key" || ok "and no more word of a key"
OUT="$(echo "$KEY" | /tmp/inbuxa install --local --domain example.test --key - --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installing again converges" || { bad "install again exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
N=$(jmap "$KEY" '[["x:Tracer/query",{},"0"]]' | grep -o '"ids":\[[^]]*\]' | tr -cd ',' | wc -c)
[ "$N" = "$TRACERS" ] && ok "without adding another tracer" || bad "tracers went from $((TRACERS + 1)) to $((N + 1))"

echo
echo "==> $pass passed, $fail failed"
[ $fail -eq 0 ]
