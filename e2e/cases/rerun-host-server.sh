#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Running install again over a server on the host: when what the server reads
# at start changed, it is restarted to read it; when nothing changed, it is
# left alone. Starts from v2026.10.12.3, which left a host server beside a host
# webmail without the webmail's secret.
#
# Run from the host with:
#   OLD_REF=v2026.10.12.3 e2e/vm/run.sh e2e/cases/rerun-host-server.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test
ARGS="--domain $D --server host --webmail host --proxy snippets --install-deps --yes"
pid() { systemctl show -p MainPID --value inbuxa-server; }
# A code for the webmail's own address is given only if the server knows the
# webmail's client, which it registers from the secret in its environment.
signs_in() {
  local user pass
  user=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
  pass=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
  curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"type\":\"authCode\",\"accountName\":\"$user\",\"accountSecret\":\"$pass\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"https://webmail.$D/api/auth/callback\",\"codeChallenge\":\"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM\",\"codeChallengeMethod\":\"S256\"}" \
    | grep -q '"client_\{0,1\}[cC]ode"'
}

echo "==> installed by the older installer"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx >/dev/null 2>&1
OUT="$(/tmp/inbuxa-old install $ARGS 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "old install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
grep -q '^INBUXA_WEBMAIL_CLIENT_SECRET=' $DIR/server.env && bad "the old installer wrote the secret after all" || ok "server.env has no webmail secret"
signs_in && bad "the server signs in for the webmail already" || ok "so the server does not sign in for the webmail"
BEFORE=$(pid)

echo
echo "==> install again, with this installer"
OUT="$(/tmp/inbuxa install $ARGS 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -8 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "re-run (exit 0)" || bad "re-run exit $rc"
grep -q '^INBUXA_WEBMAIL_CLIENT_SECRET=.' $DIR/server.env && ok "server.env has the webmail secret" || bad "still no secret"
grep -q "restarting the server" <<<"$OUT" && ok "it said it restarted the server" || bad "it did not restart the server"
[ "$(pid)" != "$BEFORE" ] && ok "the server is a new process" || bad "the server is the same process ($BEFORE)"
signs_in && ok "the server signs in for the webmail, with no restart by hand" || bad "no sign-in for the webmail"

echo
echo "==> install again, nothing changed"
BEFORE=$(pid)
OUT="$(/tmp/inbuxa install $ARGS 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "re-run (exit 0)" || bad "re-run exit $rc"
grep -q "restarting the server" <<<"$OUT" && bad "it restarted the server for nothing" || ok "it left the server alone"
[ "$(pid)" = "$BEFORE" ] && ok "the same process ($BEFORE)" || bad "the server was restarted ($BEFORE -> $(pid))"
signs_in && ok "and it still signs in for the webmail" || bad "sign-in broke"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
