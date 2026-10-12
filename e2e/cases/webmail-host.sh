#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The webmail on the host: a unit of its own on the installer's own Node,
# beside the server and the console in containers, on loopback. Checked as
# install-local checks a container webmail -- someone signs in to it -- and
# for what is particular to the host: the Node it runs on, the user it runs
# as, and that it can't write its own files.
#
# Until a webmail release carries its tarball, pass one:
#   WEBMAIL_TARBALL=path e2e/vm/run.sh e2e/cases/webmail-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
UNIT=inbuxa-webmail.service
[ -f /tmp/inbuxa-webmail-test.tar.gz ] && export INBUXA_WEBMAIL_TARBALL=/tmp/inbuxa-webmail-test.tar.gz
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }

echo "==> installing, the webmail on the host"
OUT="$(/tmp/inbuxa install --local --domain example.test --webmail host --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -16 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; journalctl -u $UNIT -n 20 --no-pager -o cat | sed 's/^/    | /'; }

echo
echo "==> the webmail is a host install"
systemctl is-active --quiet $UNIT && ok "$UNIT is running" || bad "$UNIT is not running"
PID=$(systemctl show -p MainPID --value $UNIT)
[ "$(ps -o user= -p "$PID" 2>/dev/null)" = inbuxa-webmail ] && ok "as the inbuxa-webmail user" || bad "as '$(ps -o user= -p "$PID" 2>/dev/null)'"
[ "$(readlink -f /proc/$PID/exe)" = /opt/inbuxa/node/bin/node ] && ok "on the installer's own Node: $(/opt/inbuxa/node/bin/node --version)" || bad "on $(readlink -f /proc/$PID/exe)"
/opt/inbuxa/node/bin/node --version | grep -q '^v26\.' && ok "which is Node 26, as its image" || bad "Node $(/opt/inbuxa/node/bin/node --version)"
[ "$(stat -c %U $DIR/webmail/server/dist/index.js)" = root ] && ok "its files are root's" || bad "its files are $(stat -c %U $DIR/webmail/server/dist/index.js)'s"
su -s /bin/sh inbuxa-webmail -c "touch $DIR/webmail/probe" 2>/dev/null && bad "it can write its own files" || ok "it can't write its own files (IMMUTABLE holds)"
[ "$(stat -c %a $DIR/webmail.env)" = 600 ] && ok "webmail.env is 600" || bad "webmail.env is $(stat -c %a $DIR/webmail.env)"
grep -q "^SESSION_FILE=$" $DIR/webmail.env && ok "sessions in memory (SESSION_FILE=)" || bad "SESSION_FILE is not empty"
$(rt) compose -f $DIR/compose.yaml ps --format '{{.Service}}' | grep -qx webmail && bad "a webmail container too" || ok "no webmail container"
curl -fsS http://127.0.0.1:8080/api/health | grep -q '"ok":true' && ok "it answers on 127.0.0.1:8080" || bad "no health on 8080"
[ "$(ss -ltnH 'sport = :8080' | awk '{print $4}')" = "127.0.0.1:8080" ] && ok "on loopback only" || bad "8080 is on $(ss -ltnH 'sport = :8080' | awk '{print $4}')"

echo
echo "==> someone signs in to it"
USER=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
PASS=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
rm -f /tmp/jar
LOC=$(curl -s -o /dev/null -c /tmp/jar -w '%{redirect_url}' "http://127.0.0.1:8080/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^http://127.0.0.1:8081/" <<<"$LOC" && ok "sign-in goes to the server" || bad "sign-in went to '${LOC:-nowhere}'"
q() { sed -n "s/.*[?&]$1=\([^&]*\).*/\1/p" <<<"$LOC"; }
urldecode() { printf '%b' "${1//%/\\x}"; }
STATE=$(q state); CHALLENGE=$(q code_challenge); REDIRECT=$(urldecode "$(q redirect_uri)")
ANSWER=$(curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d "{\"type\":\"authCode\",\"accountName\":\"$USER\",\"accountSecret\":\"$PASS\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"$REDIRECT\",\"codeChallenge\":\"$CHALLENGE\",\"codeChallengeMethod\":\"S256\"}")
AUTHCODE=$(sed -n 's/.*"client_\{0,1\}[cC]ode":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
BACK=$(curl -s -o /dev/null -b /tmp/jar -c /tmp/jar -w '%{redirect_url}' "$REDIRECT?code=$AUTHCODE&state=$STATE")
grep -q "signin_error" <<<"$BACK" && bad "the code exchange failed: $BACK" || ok "the webmail exchanges the code"
CODE=$(curl -s -o /tmp/login.json -b /tmp/jar -w '%{http_code}' -H 'X-Requested-With: ihasmail' http://127.0.0.1:8080/api/auth/session)
[ "$CODE" = 200 ] && grep -q "urn:ietf:params:jmap:mail" /tmp/login.json && ok "signed in, with mail" || bad "session answered $CODE"

echo
echo "==> status, and running it again"
/tmp/inbuxa status 2>&1 | grep -q "webmail.*installed as a host" && ok "status: the webmail is a host install" || bad "status does not say so"
OUT="$(/tmp/inbuxa install --local --domain example.test --webmail host --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "a second run converges" || { bad "second run exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
systemctl is-active --quiet $UNIT && ok "and the webmail is still running" || bad "the webmail stopped"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
