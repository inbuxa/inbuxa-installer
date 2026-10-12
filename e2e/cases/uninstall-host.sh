#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa uninstall, the server and the webmail on the host: units, binaries,
# the webmail's files and user and the installer's Node go; the server's
# directory and user stay, and install run again brings the server back on
# them; --purge takes those too.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/uninstall-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
DIR=/var/lib/inbuxa
signs_in() {
  local user pass
  user=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
  pass=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
  curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"type\":\"authCode\",\"accountName\":\"$user\",\"accountSecret\":\"$pass\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"http://127.0.0.1:8080/api/auth/callback\",\"codeChallenge\":\"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM\",\"codeChallengeMethod\":\"S256\"}" \
    | grep -q '"client_\{0,1\}[cC]ode"'
}
user() { getent passwd "$1" >/dev/null; }
INSTALL=(/tmp/inbuxa install --local --domain example.test --server host --webmail host --install-deps --yes)

echo "==> installed on the host"
"${INSTALL[@]}" >/dev/null 2>&1 && ok "installed" || bad "install"
signs_in && ok "the first mailbox signs in" || bad "no sign-in to start with"

echo
echo "==> uninstalling"
OUT="$(/tmp/inbuxa uninstall --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "uninstalled (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
for u in inbuxa-server inbuxa-webmail; do
  [ ! -f /etc/systemd/system/$u.service ] && ! systemctl is-active --quiet $u && ok "$u is gone" || bad "$u is still there"
done
[ ! -e /usr/local/bin/inbuxa-server ] && ok "the server's binary is gone" || bad "the binary is still there"
[ ! -e $DIR/webmail ] && ! user inbuxa-webmail && ok "the webmail's files and user are gone" || bad "webmail files or user left"
[ ! -e /opt/inbuxa/node ] && ok "the installer's Node is gone" || bad "/opt/inbuxa/node is still there"
[ -f $DIR/server/config.json ] && user inbuxa && ok "the server's directory and user are kept" || bad "the server's data or user went"

echo
echo "==> installed again, on what was kept"
OUT="$("${INSTALL[@]}" 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "install again (exit 0)" || { bad "reinstall exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
for _ in $(seq 1 30); do signs_in && break; sleep 2; done
signs_in && ok "the same mailbox signs in" || bad "the first mailbox no longer signs in"
curl -fsS http://127.0.0.1:8080/api/health | grep -q '"ok":true' && ok "the webmail is back" || bad "no webmail"

echo
echo "==> purged"
OUT="$(/tmp/inbuxa uninstall --purge --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "purged (exit 0)" || bad "purge exit $rc"
[ ! -e $DIR ] && ! user inbuxa && ok "$DIR and the inbuxa user are gone" || bad "left: $(ls -A $DIR 2>/dev/null | tr '\n' ' ') user=$(user inbuxa && echo yes)"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
