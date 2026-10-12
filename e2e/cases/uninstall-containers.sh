#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa uninstall, everything in containers: the programs go and the data
# stays; install run again brings it back with the same mailbox; --purge
# then removes the data too, all but the backups.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/uninstall-containers.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
DIR=/var/lib/inbuxa
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
containers() { $(rt) ps -a --filter label=com.docker.compose.project=inbuxa --format '{{.Names}}' | wc -l; }
volumes() { $(rt) volume ls --format '{{.Name}}' | grep -c '^inbuxa_'; }
signs_in() {
  local user pass
  user=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
  pass=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
  curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"type\":\"authCode\",\"accountName\":\"$user\",\"accountSecret\":\"$pass\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"http://127.0.0.1:8080/api/auth/callback\",\"codeChallenge\":\"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM\",\"codeChallengeMethod\":\"S256\"}" \
    | grep -q '"client_\{0,1\}[cC]ode"'
}

echo "==> installed, with an archive from an upgrade"
/tmp/inbuxa install --local --domain example.test --install-deps --yes >/dev/null 2>&1 && ok "installed" || bad "install"
mkdir -p $DIR/backups && echo archive > $DIR/backups/server-test.tar.gz
signs_in && ok "the first mailbox signs in" || bad "no sign-in to start with"

echo
echo "==> what it would do"
DRY="$(/tmp/inbuxa uninstall --dry-run 2>&1)"; rc=$?
grep -q "inbuxa_inbuxa-data" <<<"$(sed -n '/^Kept:/,$p' <<<"$DRY")" && ok "the server's volumes are kept" || { bad "dry run"; echo "$DRY" | sed 's/^/    /'; }
[ "$(containers)" = 3 ] && ok "a dry run changed nothing" || bad "containers: $(containers)"
OUT="$(/tmp/inbuxa uninstall 2>&1)"; rc=$?
[ $rc -ne 0 ] && [ "$(containers)" = 3 ] && ok "without --yes, nothing happens" || bad "it ran without --yes"
OUT="$(/tmp/inbuxa uninstall console --yes 2>&1)"; rc=$?
[ $rc -eq 2 ] && grep -q -- "--console skip" <<<"$OUT" && ok "one component points to install --console skip" || bad "uninstall console: exit $rc"

echo
echo "==> uninstalling"
OUT="$(/tmp/inbuxa uninstall --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "uninstalled (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
[ "$(containers)" = 0 ] && ok "no containers left" || bad "$(containers) containers left"
[ "$(volumes)" -ge 2 ] && ok "the volumes are kept ($(volumes))" || bad "the volumes are gone"
[ -f $DIR/credentials.txt ] && [ -f $DIR/.env ] && ok "credentials.txt and .env are kept" || bad "the credentials are gone"
[ ! -f $DIR/compose.yaml ] && ok "the compose file is gone" || bad "compose.yaml is still there"
/tmp/inbuxa status 2>&1 | grep -q "uninstalled" && ok "status says it was uninstalled, data kept" || bad "status: $(/tmp/inbuxa status 2>&1 | head -2)"
/tmp/inbuxa upgrade --yes >/dev/null 2>&1 && bad "upgrade ran on nothing" || ok "upgrade has nothing to do"

echo
echo "==> installed again, on what was kept"
OUT="$(/tmp/inbuxa install --local --domain example.test --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "install again (exit 0)" || { bad "reinstall exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
[ "$(containers)" = 3 ] && ok "three containers again" || bad "$(containers) containers"
for _ in $(seq 1 30); do signs_in && break; sleep 2; done
signs_in && ok "the same mailbox signs in: the data came back" || bad "the first mailbox no longer signs in"
/tmp/inbuxa status >/dev/null 2>&1 && ok "status: installed and in agreement" || bad "status after reinstall"

echo
echo "==> purged"
OUT="$(/tmp/inbuxa uninstall --purge --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "purged (exit 0)" || { bad "purge exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
[ "$(containers)" = 0 ] && [ "$(volumes)" = 0 ] && ok "no containers, no volumes" || bad "containers $(containers), volumes $(volumes)"
[ "$(ls -A $DIR)" = backups ] && ok "only $DIR/backups is left" || bad "left in $DIR: $(ls -A $DIR | tr '\n' ' ')"
[ ! -f /etc/inbuxa/install.json ] && ok "the install record is gone" || bad "install.json is still there"
/tmp/inbuxa status 2>&1 | grep -q "Nothing is installed" && ok "status: nothing installed" || bad "status after purge"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
