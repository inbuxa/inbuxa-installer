#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The whole thing, on a machine with nothing on it: install the suite in
# containers, loopback only, and then prove it is a working mail system rather
# than three containers that happen to be running.
#
# The checks that matter most are the last two: that the credential used to
# bootstrap the server does not outlive the setup, and that the account the
# installer created can actually sign in to the webmail it installed. Either
# one failing means the install looked fine and was not.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/install-local.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
has()  { grep -q -- "$2" <<<"$1" && ok "$3" || { bad "$3"; echo "$1" | tail -20 | sed 's/^/    /'; }; }

DIR=/var/lib/inbuxa

# Whichever runtime this machine has. The installer picks docker where there
# is one and podman on the Red Hat family; a case that says "docker" only
# tests half the distributions it is run on.
#
# Asked at each call, not once at the top: these cases start on a machine
# with no runtime at all and install one along the way, so anything decided
# up here is decided before the answer exists.
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}

echo "==> installing"
OUT="$(/tmp/inbuxa install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -24 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"

echo
echo "==> what it left on disk"
[ -f "$DIR/compose.yaml" ] && ok "compose.yaml, which is the deployment" || bad "no compose.yaml"
[ -f "$DIR/credentials.txt" ] && ok "credentials.txt" || bad "no credentials.txt"
[ -f "$DIR/dns.zone" ] && ok "dns.zone" || bad "no dns.zone"
[ "$(stat -c %a "$DIR/.env" 2>/dev/null)" = 600 ] && ok ".env is 600" || bad ".env is $(stat -c %a "$DIR/.env" 2>/dev/null), not 600"
[ "$(stat -c %a "$DIR/credentials.txt")" = 600 ] && ok "credentials.txt is 600" || bad "credentials.txt is $(stat -c %a "$DIR/credentials.txt"), not 600"
grep -q "MX" "$DIR/dns.zone" && ok "the zone file has the MX record" || bad "no MX in the zone file"
grep -q "_domainkey" "$DIR/dns.zone" && ok "and the DKIM key it generated" || bad "no DKIM record"

echo
echo "==> what it left running"
STATE="$(compose ps --format '{{.Service}} {{.State}}')"
for s in server console webmail; do
  grep -q "^$s running" <<<"$STATE" && ok "$s is running" || { bad "$s is not running"; echo "$STATE" | sed 's/^/    /'; }
done

echo
echo "==> and it answers"
curl -fsS -o /dev/null http://127.0.0.1:8081/.well-known/jmap -w '' 2>/dev/null
[ $? -le 22 ] && ok "the mail server answers on its loopback bind" || bad "the mail server does not answer"
curl -fsS http://127.0.0.1:8082/ 2>/dev/null | grep -q 'api-base-url' && ok "the console is served, pointed at the server" || bad "the console is not served"
curl -fsS http://127.0.0.1:8080/api/health 2>/dev/null | grep -q '"ok":true' && ok "the webmail is healthy" || bad "the webmail is not healthy"

echo
echo "==> the bootstrap credential did not outlive the setup"
ENVOUT="$("$(rt)" inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$(compose ps -q server)")"
grep -q "RECOVERY_ADMIN" <<<"$ENVOUT" && { bad "the server still carries a recovery admin"; echo "$ENVOUT" | grep RECOVERY | sed 's/^/    /'; } || ok "no recovery admin in the running server"
grep -q "INBUXA_WEBMAIL_CLIENT_SECRET" <<<"$ENVOUT" && ok "the webmail's client secret is where it belongs" || bad "the server has no webmail client secret"
# The setup's API key was made in a window where the server took passwords
# on /jmap. Afterwards it must take tokens only again (contract C-23).
grep -q "HTTP_BASIC_AUTH" <<<"$ENVOUT" && bad "the server still takes passwords everywhere" || ok "no Basic-everywhere override in the running server"
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
# Sign-in is the mail server's own page here too, as on a public install:
# the server takes no password on /jmap (contract C-23). This does what a
# browser does -- start at the webmail, post the password where the server's
# page posts it, follow the code back -- so a pass means a person could.
rm -f /tmp/jar
LOC=$(curl -s -o /dev/null -c /tmp/jar -w '%{redirect_url}' "http://127.0.0.1:8080/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^http://127.0.0.1:8081/" <<<"$LOC" && ok "the webmail sends sign-in to the server's loopback address" || bad "sign-in went to '${LOC:-nowhere}'"
grep -q "client_id=ihasmail-inbuxa" <<<"$LOC" && ok "as the first-party client" || bad "no first-party client id in '$LOC'"
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
echo "==> running it again converges rather than duplicating"
OUT="$(/tmp/inbuxa install --local --domain example.test --yes 2>&1)"; rc=$?
COUNT=$(compose ps --format '{{.Service}}' | sort -u | wc -l)
[ "$COUNT" = 3 ] && ok "still three services, not six" || bad "$COUNT services after a second run"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
