#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The mail server on the host, the console and the webmail in containers
# beside it, on loopback: the same whole system install-local.sh proves, with
# the server as a release binary under systemd instead of a container.
#
# What is particular to the host is checked as well as what is common: the
# setup's drop-in and credential are gone, the server runs as its own user
# with nothing on 443 or on 8080 of its own, the unit refuses to start
# without a configuration, and status says where the server is.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/install-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
UNIT=inbuxa-server.service

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
listening() { ss -ltnH "sport = :$1" | awk '{print $4}'; }

echo "==> what it would do"
PLAN="$(/tmp/inbuxa install --local --domain example.test --server host --install-deps --dry-run 2>&1)"
grep -q "Install the mail server on this machine" <<<"$PLAN" && ok "the plan names the host install" || { bad "no host install in the plan"; echo "$PLAN" | sed 's/^/    /'; }
grep -q "/usr/local/bin/inbuxa-server, release" <<<"$PLAN" && ok "and the release it fetches" || bad "the plan does not name the release"

echo
echo "==> installing"
OUT="$(/tmp/inbuxa install --local --domain example.test --server host --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -30 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"

echo
echo "==> the server is a host install"
/usr/local/bin/inbuxa-server --version 2>&1 | grep -q '^[0-9]\{2,4\}\.[0-9]' && ok "the release binary runs: $(/usr/local/bin/inbuxa-server --version 2>&1 | head -1)" || bad "no runnable /usr/local/bin/inbuxa-server"
systemctl is-active --quiet $UNIT && ok "$UNIT is running" || { bad "$UNIT is not running"; journalctl -u $UNIT -n 20 --no-pager | sed 's/^/    /'; }
systemctl is-enabled --quiet $UNIT && ok "and enabled, so it comes back after a reboot" || bad "$UNIT is not enabled"
PID=$(systemctl show -p MainPID --value $UNIT)
[ "$(ps -o user= -p "$PID" 2>/dev/null)" = inbuxa ] && ok "it runs as the inbuxa user" || bad "it runs as '$(ps -o user= -p "$PID" 2>/dev/null)'"
[ -f "$DIR/server/config.json" ] && ok "its configuration is in $DIR/server" || bad "no $DIR/server/config.json"
[ "$(stat -c %U "$DIR/server/data")" = inbuxa ] && ok "and its data directory is its own" || bad "$DIR/server/data is owned by $(stat -c %U "$DIR/server/data")"
grep -q "^ConditionPathExists=$DIR/server/config.json" /etc/systemd/system/$UNIT && ok "the finished unit needs the configuration to start" || bad "no ConditionPathExists in the unit"
grep -q "^AmbientCapabilities=CAP_NET_BIND_SERVICE" /etc/systemd/system/$UNIT && ok "and may bind the ports below 1024" || bad "no AmbientCapabilities in the unit"
compose ps --format '{{.Service}}' | grep -qx server && bad "a server container is running too" || ok "no server container"

echo
echo "==> the setup left nothing behind"
[ ! -e /run/systemd/system/$UNIT.d/inbuxa-setup.conf ] && ok "the setup's drop-in is gone" || bad "the setup's drop-in is still there"
[ ! -e /run/inbuxa-server-setup.env ] && ok "and so is its credential file" || bad "the setup's credential file is still there"
ENVOUT="$(tr '\0' '\n' < /proc/"$PID"/environ)"
grep -q "RECOVERY_ADMIN" <<<"$ENVOUT" && bad "the server still carries a recovery admin" || ok "no recovery admin in the running server"
grep -q "HTTP_BASIC_AUTH" <<<"$ENVOUT" && bad "the server still takes passwords everywhere" || ok "no Basic-everywhere override in the running server"
grep -q "INBUXA_WEBMAIL_CLIENT_SECRET" <<<"$ENVOUT" && ok "the webmail's client secret is in its environment" || bad "the server has no webmail client secret"
[ "$(stat -c %a "$DIR/server.env")" = 600 ] && ok "server.env is 600" || bad "server.env is $(stat -c %a "$DIR/server.env")"
[ "$(stat -c %a "$DIR/credentials.txt")" = 600 ] && ok "credentials.txt is 600" || bad "credentials.txt is $(stat -c %a "$DIR/credentials.txt")"

echo
echo "==> its listeners fit the host"
[ "$(listening 8081)" = "127.0.0.1:8081" ] && ok "plain HTTP on 127.0.0.1:8081 only" || bad "8081 is on '$(listening 8081)'"
[ -z "$(listening 443)" ] && ok "nothing on 443: that is the proxy's" || bad "443 is held: $(listening 443)"
[ "$(listening 8080)" = "127.0.0.1:8080" ] && ok "8080 is the webmail's, on loopback" || bad "8080 is on '$(listening 8080)'"
for p in 25 465 993 995 4190; do
  [ "$(listening $p)" = "127.0.0.1:$p" ] || { bad "port $p is on '$(listening $p)', not loopback"; continue; }
done
ok "the mail ports are on loopback, as a local install publishes nothing"

echo
echo "==> and it answers"
curl -fsS -o /dev/null http://127.0.0.1:8081/.well-known/jmap 2>/dev/null
[ $? -le 22 ] && ok "the mail server answers on its loopback bind" || bad "the mail server does not answer"
curl -fsS http://127.0.0.1:8082/ 2>/dev/null | grep -q 'api-base-url' && ok "the console is served, pointed at the server" || bad "the console is not served"
curl -fsS http://127.0.0.1:8080/api/health 2>/dev/null | grep -q '"ok":true' && ok "the webmail is healthy" || bad "the webmail is not healthy"
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
CODE=$(curl -s -o /dev/null -w '%{http_code}' -u "$AUSER:$APASS" -H 'Content-Type: application/json' \
  -d '{"using":["urn:ietf:params:jmap:core"],"methodCalls":[]}' http://127.0.0.1:8081/jmap/)
[ "$CODE" = 401 ] && ok "the administrator's password is refused on /jmap" || bad "the administrator's password on /jmap answered $CODE, not 401"

echo
echo "==> the account it created can sign in to the webmail it installed"
USER=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
PASS=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
[ -n "$USER" ] && ok "credentials.txt names the first mailbox ($USER)" || bad "no first mailbox in credentials.txt"
rm -f /tmp/jar
LOC=$(curl -s -o /dev/null -c /tmp/jar -w '%{redirect_url}' "http://127.0.0.1:8080/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^http://127.0.0.1:8081/" <<<"$LOC" && ok "the webmail sends sign-in to the server's loopback address" || bad "sign-in went to '${LOC:-nowhere}'"
q() { sed -n "s/.*[?&]$1=\([^&]*\).*/\1/p" <<<"$LOC"; }
urldecode() { printf '%b' "${1//%/\\x}"; }
STATE=$(q state); CHALLENGE=$(q code_challenge); REDIRECT=$(urldecode "$(q redirect_uri)")
ANSWER=$(curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d "{\"type\":\"authCode\",\"accountName\":\"$USER\",\"accountSecret\":\"$PASS\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"$REDIRECT\",\"codeChallenge\":\"$CHALLENGE\",\"codeChallengeMethod\":\"S256\"}")
AUTHCODE=$(sed -n 's/.*"client_\{0,1\}[cC]ode":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
[ -n "$AUTHCODE" ] && ok "the server's sign-in takes the first mailbox's password" || { bad "the server's sign-in answered"; head -c 200 <<<"$ANSWER" | sed 's/^/    /'; }
BACK=$(curl -s -o /dev/null -b /tmp/jar -c /tmp/jar -w '%{redirect_url}' "$REDIRECT?code=$AUTHCODE&state=$STATE")
grep -q "signin_error" <<<"$BACK" && bad "the webmail refused the code: $BACK" || ok "the webmail takes the code back"
CODE=$(curl -s -o /tmp/login.json -b /tmp/jar -w '%{http_code}' -H 'X-Requested-With: ihasmail' http://127.0.0.1:8080/api/auth/session)
[ "$CODE" = 200 ] && ok "sign-in succeeds" || { bad "the session answered $CODE"; head -c 200 /tmp/login.json | sed 's/^/    /'; }
grep -q "urn:ietf:params:jmap:mail" /tmp/login.json && ok "and the session carries the mail capability" || bad "no mail capability in the session"

echo
echo "==> status knows where the server is"
STATUS="$(/tmp/inbuxa status 2>&1)"; rc=$?
grep -q "server.*installed as a host" <<<"$STATUS" && ok "status: the server is a host install" || { bad "status does not say so"; echo "$STATUS" | sed 's/^/    /'; }
[ $rc -eq 0 ] && ok "and intent and reality agree (exit 0)" || { bad "status exit $rc"; echo "$STATUS" | sed 's/^/    /'; }
/tmp/inbuxa export 2>/dev/null | grep -A1 '"kind": "server"' | grep -q '"shape": "host"' && ok "export writes the server as host" || bad "export does not say host"

echo
echo "==> running it again converges rather than duplicating"
OUT="$(/tmp/inbuxa install --local --domain example.test --server host --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "a second run succeeds" || { bad "second run exit $rc"; echo "$OUT" | tail -10 | sed 's/^/    /'; }
grep -q "is already" <<<"$OUT" && ok "and keeps the binary it has" || bad "the second run fetched the binary again"
COUNT=$(compose ps --format '{{.Service}}' | sort -u | wc -l)
[ "$COUNT" = 2 ] && ok "still two containers beside the server" || bad "$COUNT containers after a second run"
# The move itself is move-server.sh's; without a key it changes nothing.
OUT="$(INBUXA_API_KEY= /tmp/inbuxa install --local --domain example.test --server container --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "API key is needed" <<<"$OUT" && ok "moving the server into a container without a key is refused, with the reason" || bad "a move without a key was not refused: exit $rc"
systemctl is-active --quiet inbuxa-server && ok "and the server is still on the host" || bad "the server stopped"

echo
echo "==> a server without its configuration stays down"
mv "$DIR/server/config.json" /tmp/config.json.kept
systemctl restart $UNIT; sleep 2
systemctl is-active --quiet $UNIT && bad "the server started without a configuration" || ok "it does not start in bootstrap mode"
[ -z "$(listening 8080 | grep -v 127.0.0.1:8080)" ] && ok "and nothing opened 8080 on another interface" || bad "8080 is open: $(listening 8080)"
mv /tmp/config.json.kept "$DIR/server/config.json"; chown inbuxa:inbuxa "$DIR/server/config.json"
systemctl restart $UNIT
# Any HTTP answer at all; 000 is curl failing to connect.
code() { curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/.well-known/jmap; }
for _ in $(seq 1 60); do [ "$(code)" != 000 ] && break; sleep 2; done
code | grep -qE '^(200|401|30[0-9])$' && ok "with it back, the server answers again" || { bad "the server did not come back ($(code))"; journalctl -u $UNIT -n 15 --no-pager | sed 's/^/    /'; }

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
