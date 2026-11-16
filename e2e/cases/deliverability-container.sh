#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The server's Deliverability check on a container install. The container is
# named after the mail host, so /etc/hosts inside it maps that name to the
# container's own addresses on the installer's network. Those are no address
# a receiver ever sees, and the check must not test them against SPF
# (inbuxa-admin#224, inbuxa-server#408, fixed in server 27.1-rc.25).
#
# Run from the host with: e2e/vm/run.sh e2e/cases/deliverability-container.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
SERVER=http://127.0.0.1:8081
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
    -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"urn:inbuxa:jmap\",\"urn:inbuxa:jmap:registry\"],\"methodCalls\":$2}" "$SERVER/jmap/"
}
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }

echo "==> installed in containers"
OUT="$(/tmp/inbuxa install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed" || { bad "install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
OWN="$(compose exec -T server getent hosts "$(compose exec -T server hostname 2>/dev/null | tr -d '\r')" 2>/dev/null | awk '{print $1}' | tr '\n' ' ')"
[ -n "$OWN" ] && ok "inside the container its name is $OWN" || ok "the container's name has no /etc/hosts entry to trip over"

echo
echo "==> an API key, as an administrator makes one in the console"
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
printf 'services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n' > /tmp/basic.yaml
compose -f /tmp/basic.yaml up -d server >/dev/null 2>&1; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"deliverability test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
compose up -d server >/dev/null 2>&1; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }
ACCOUNT=$(curl -s -H "Authorization: Bearer $KEY" $SERVER/jmap/session | sed -n 's/.*"primaryAccounts":{[^}]*"urn:ietf:params:jmap:core":"\([^"]*\)".*/\1/p')
[ -n "$ACCOUNT" ] || ACCOUNT=$(curl -s -H "Authorization: Bearer $KEY" $SERVER/jmap/session | grep -o '"accounts":{"[^"]*"' | sed 's/.*{"//; s/"$//')
[ -n "$ACCOUNT" ] && ok "its account is $ACCOUNT" || bad "no account in the session"

echo
echo "==> Check now"
report() { jmap "$KEY" "[[\"inbuxa:DeliverabilityReport/get\",{\"accountId\":\"$ACCOUNT\",\"ids\":null},\"0\"]]"; }
BEFORE=$(report | grep -o '"checkedAt":"[^"]*"' | head -1)
NOW=$(jmap "$KEY" "[[\"inbuxa:DeliverabilityReport/set\",{\"accountId\":\"$ACCOUNT\",\"create\":{\"now\":{}}},\"0\"]]")
grep -q '"created"' <<<"$NOW" && ok "a check is queued" || { bad "Check now refused"; head -c 300 <<<"$NOW" | sed 's/^/    /'; }
for _ in $(seq 1 60); do
  R=$(report); AFTER=$(grep -o '"checkedAt":"[^"]*"' <<<"$R" | head -1)
  [ -n "$AFTER" ] && [ "$AFTER" != "$BEFORE" ] && break; sleep 3
done
[ -n "$AFTER" ] && [ "$AFTER" != "$BEFORE" ] && ok "the node checked itself: $AFTER" || { bad "no new report"; head -c 400 <<<"$R" | sed 's/^/    /'; }
ADDRS=$(grep -o '"addresses":\[[^]]*\]' <<<"$R" | grep -o '"ip":"[^"]*"' | sed 's/"ip":"//; s/"//' | tr '\n' ' ')
echo "    addresses checked: ${ADDRS:-none}"
grep -qE '"ip":"(172\.31\.253\.|fd69:6e62:7578:|10\.|192\.168\.|127\.)' <<<"$R" \
  && bad "the report tests the container's own address: $ADDRS" || ok "none of the container's own addresses are tested"
grep -qE "SPF[^\"]*(172\.31\.253\.|fd69:6e62:7578:)" <<<"$R" && bad "an SPF result names a container address" || ok "and no SPF result names one"

echo
echo "==> $pass passed, $fail failed"
[ $fail -eq 0 ]
