#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa join: the console and the webmail on one machine, for a server
# installed on another. Two machines are played by one: the server is
# installed on its own, its install record is set aside so this looks like a
# machine without one, and the front ends are joined to it on loopback in a
# deployment directory of their own.
#
# What only a join proves is checked: the server learns the front ends'
# addresses over JMAP (inbuxa:FrontEnds), the console's origin is allowed,
# and a person signs in to the joined webmail with the secret made here --
# which the server never saw except as the one request that set it.
#
# Needs a server with inbuxa:FrontEnds; until a release has it, pass a build:
#   SERVER_BINARY=path/to/inbuxa e2e/vm/run.sh e2e/cases/join.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

SERVER_DIR=/var/lib/inbuxa
DIR=/var/lib/inbuxa-joined
SERVER=http://127.0.0.1:8081
if [ -x /tmp/inbuxa-server-test ]; then
  export INBUXA_SERVER_BINARY=/tmp/inbuxa-server-test
  # A debug build needs a deeper stack than a release one, as the server's
  # own test suites do (RUST_MIN_STACK in its CI). A drop-in made before the
  # unit exists applies when it does.
  mkdir -p /etc/systemd/system/inbuxa-server.service.d
  printf '[Service]\nEnvironment=RUST_MIN_STACK=16777216\n' > /etc/systemd/system/inbuxa-server.service.d/test-build.conf
fi

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
jmap() { # jmap TOKEN-or-user:pass CAPABILITY JSON-CALLS
  local auth
  case "$1" in *:*) auth=(-u "$1") ;; *) auth=(-H "Authorization: Bearer $1") ;; esac
  curl -s "${auth[@]}" -H 'Content-Type: application/json' \
    -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"$2\"],\"methodCalls\":$3}" "$SERVER/jmap/"
}
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }

echo "==> the server, on its own"
OUT="$(/tmp/inbuxa install --local --domain example.test --server host --console skip --webmail skip --yes 2>&1)"; rc=$?
if [ $rc -eq 0 ]; then ok "installed the server alone"; else
  bad "server install exit $rc"; echo "$OUT" | tail -15 | sed 's/^/    /'
  journalctl -u inbuxa-server -n 30 --no-pager -o cat | sed 's/^/    | /'
  echo; echo "==> $pass passed, $fail failed (stopped: no server to join)"; exit 1
fi
[ -n "${INBUXA_SERVER_BINARY:-}" ] && echo "    (server: $(/usr/local/bin/inbuxa-server --version 2>&1 | head -1), from the build given)"

echo
echo "==> an API key, as an administrator makes one in the console"
# The console is a browser; here the server takes the administrator's
# password for one restart, the way the installer's own setup does.
AUSER=$(awk '/^administrator/ {print $2}' "$SERVER_DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$SERVER_DIR/credentials.txt")
echo "INBUXA_HTTP_BASIC_AUTH=all" >> "$SERVER_DIR/server.env"
systemctl restart inbuxa-server; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" urn:inbuxa:jmap:registry "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"join test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
sed -i '/^INBUXA_HTTP_BASIC_AUTH=/d' "$SERVER_DIR/server.env"
systemctl restart inbuxa-server; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }

ACCOUNT=$(curl -s -H "Authorization: Bearer $KEY" $SERVER/jmap/session | grep -o '"primaryAccounts":{[^}]*}' | sed -n 's/^"primaryAccounts":{"[^"]*":"\([^"]*\)".*/\1/p')
[ -n "$ACCOUNT" ] || bad "no account id in the key's session"

# From here this is "another machine": no install recorded on it.
mv /etc/inbuxa/install.json /etc/inbuxa/install.server.json

echo
echo "==> what join would do, asking the server first"
OUT="$(INBUXA_JOIN_KEY=wrong-key /tmp/inbuxa join $SERVER --local --dir $DIR --install-deps --dry-run 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "API key" <<<"$OUT" && ok "a wrong key is refused, and says so" || { bad "a wrong key: exit $rc"; echo "$OUT" | tail -5 | sed 's/^/    /'; }
OUT="$(INBUXA_JOIN_KEY=$KEY /tmp/inbuxa join $SERVER --local --dir $DIR --install-deps --dry-run 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "the key works" <<<"$OUT" && ok "the dry run asks the server, and changes nothing" || { bad "dry run exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
grep -q "Register these front ends with the server" <<<"$OUT" && ok "and the plan names the registration" || bad "no registration step in the plan"
FE=$(jmap "$KEY" urn:inbuxa:jmap '[["inbuxa:FrontEnds/get",{"accountId":"'"$ACCOUNT"'"},"0"]]')
grep -q '"adminUrl":null' <<<"$FE" && ok "the server still lists no console" || bad "a dry run changed the server: $FE"

echo
echo "==> joining"
OUT="$(echo "$KEY" | /tmp/inbuxa join $SERVER --key - --local --dir $DIR --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -24 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "joined (exit 0)" || bad "exit $rc"
grep -q "$KEY" <<<"$(cat $DIR/* 2>/dev/null)" && bad "the API key was written down here" || ok "the API key was not kept"
STATE="$(compose ps --format '{{.Service}} {{.State}}')"
for s in console webmail; do
  grep -q "^$s running" <<<"$STATE" && ok "$s is running" || { bad "$s is not running"; echo "$STATE" | sed 's/^/    /'; }
done
grep -q "^  server:" "$DIR/compose.yaml" && bad "a server was put here too" || ok "no server here"
grep -q '"serverUrl": "http://127.0.0.1:8081"' /etc/inbuxa/install.json && ok "the install record names the server it joined" || bad "no serverUrl in the install record"

echo
echo "==> the server knows the front ends"
FE=$(jmap "$KEY" urn:inbuxa:jmap '[["inbuxa:FrontEnds/get",{"accountId":"'"$ACCOUNT"'"},"0"]]')
grep -q '"adminUrl":"http://127.0.0.1:8082"' <<<"$FE" && ok "the console's address" || bad "adminUrl: $FE"
grep -q '"webmailUrl":"http://127.0.0.1:8080"' <<<"$FE" && ok "the webmail's address" || bad "webmailUrl: $FE"
grep -q '"webmailClientSecretSet":true' <<<"$FE" && ok "and a webmail secret, which it does not show" || bad "no webmail secret: $FE"
ALLOW=$(curl -s -o /dev/null -D - -X OPTIONS $SERVER/jmap/session -H 'Origin: http://127.0.0.1:8082' \
  -H 'Access-Control-Request-Method: GET' -H 'Access-Control-Request-Headers: authorization' | tr -d '\r' | awk -F': ' 'tolower($1)=="access-control-allow-origin"{print $2}')
[ "$ALLOW" = "http://127.0.0.1:8082" ] && ok "the console's origin may call the server" || bad "access-control-allow-origin: '$ALLOW'"
curl -fsS http://127.0.0.1:8082/ 2>/dev/null | grep -q "content=\"$SERVER\"" && ok "the console points at the server" || bad "the console's api-base-url is not $SERVER"

echo
echo "==> a person signs in to the joined webmail"
USER=$(awk '/^first mailbox/ {print $3}' "$SERVER_DIR/credentials.txt")
PASS=$(awk '/^first mailbox/{getline; print $2}' "$SERVER_DIR/credentials.txt")
rm -f /tmp/jar
LOC=$(curl -s -o /dev/null -c /tmp/jar -w '%{redirect_url}' "http://127.0.0.1:8080/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^$SERVER/" <<<"$LOC" && ok "the webmail sends sign-in to the server" || bad "sign-in went to '${LOC:-nowhere}'"
q() { sed -n "s/.*[?&]$1=\([^&]*\).*/\1/p" <<<"$LOC"; }
urldecode() { printf '%b' "${1//%/\\x}"; }
STATE=$(q state); CHALLENGE=$(q code_challenge); REDIRECT=$(urldecode "$(q redirect_uri)")
ANSWER=$(curl -s -X POST $SERVER/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d "{\"type\":\"authCode\",\"accountName\":\"$USER\",\"accountSecret\":\"$PASS\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"$REDIRECT\",\"codeChallenge\":\"$CHALLENGE\",\"codeChallengeMethod\":\"S256\"}")
AUTHCODE=$(sed -n 's/.*"client_\{0,1\}[cC]ode":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
[ -n "$AUTHCODE" ] && ok "the server takes the joined webmail's redirect" || { bad "the server's sign-in answered"; head -c 200 <<<"$ANSWER" | sed 's/^/    /'; }
BACK=$(curl -s -o /dev/null -b /tmp/jar -c /tmp/jar -w '%{redirect_url}' "$REDIRECT?code=$AUTHCODE&state=$STATE")
grep -q "signin_error" <<<"$BACK" && bad "the webmail's code exchange failed (its secret?): $BACK" || ok "the webmail exchanges the code with the secret made here"
CODE=$(curl -s -o /tmp/login.json -b /tmp/jar -w '%{http_code}' -H 'X-Requested-With: ihasmail' http://127.0.0.1:8080/api/auth/session)
[ "$CODE" = 200 ] && grep -q "urn:ietf:params:jmap:mail" /tmp/login.json && ok "signed in, with mail" || { bad "the session answered $CODE"; head -c 200 /tmp/login.json | sed 's/^/    /'; }

echo
echo "==> again, and differently"
OUT="$(INBUXA_JOIN_KEY=$KEY /tmp/inbuxa join $SERVER --local --dir $DIR --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "joining again with the same addresses converges" || { bad "second join exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
COUNT=$(compose ps --format '{{.Service}}' | sort -u | wc -l)
[ "$COUNT" = 2 ] && ok "still two containers" || bad "$COUNT containers after a second join"
OUT="$(INBUXA_JOIN_KEY=$KEY /tmp/inbuxa join $SERVER --domain example.test --dir /tmp/elsewhere --dry-run 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q -- "--replace" <<<"$OUT" && ok "a console elsewhere is refused without --replace" || { bad "a different console was not refused: exit $rc"; echo "$OUT" | tail -4 | sed 's/^/    /'; }

mv /etc/inbuxa/install.server.json /etc/inbuxa/install.json.server-kept 2>/dev/null
echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
