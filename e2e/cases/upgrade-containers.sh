#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa upgrade, everything in containers: installed by an older installer,
# moved to this one's releases, the server's data archived first, and the
# same mailbox still signing in afterwards.
#
# Run from the host with:
#   OLD_REF=v2026.10.12.1 e2e/vm/run.sh e2e/cases/upgrade-containers.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
DIR=/var/lib/inbuxa
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
img() { grep -o "inbuxa-$1:[^ ]*" $DIR/compose.yaml; }
signs_in() {
  local user pass
  user=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
  pass=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
  curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"type\":\"authCode\",\"accountName\":\"$user\",\"accountSecret\":\"$pass\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"http://127.0.0.1:8080/api/auth/callback\",\"codeChallenge\":\"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM\",\"codeChallengeMethod\":\"S256\"}" \
    | grep -q '"client_\{0,1\}[cC]ode"'
}

echo "==> installed by the older installer"
OUT="$(/tmp/inbuxa-old install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed with $(/tmp/inbuxa-old version)" || { bad "old install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
BEFORE="$(img server) $(img admin) $(img webmail)"
echo "    $BEFORE"
signs_in && ok "the first mailbox signs in" || bad "the first mailbox does not sign in before"

echo
echo "==> what this installer says about it"
STATUS="$(/tmp/inbuxa status 2>&1)"
grep -q "this installer installs" <<<"$STATUS" && ok "status says newer releases are here" || { bad "status says nothing about versions"; echo "$STATUS" | sed 's/^/    /'; }
DRY="$(/tmp/inbuxa upgrade --dry-run 2>&1)"; rc=$?
[ $rc -eq 0 ] && [ "$(grep -c -- ' -> ' <<<"$DRY")" = 3 ] && ok "upgrade --dry-run shows three to move" || { bad "dry run exit $rc"; echo "$DRY" | sed 's/^/    /'; }
grep -q "archived into" <<<"$DRY" && ok "and that the server's data is archived first" || bad "no word of the backup"
[ "$BEFORE" = "$(img server) $(img admin) $(img webmail)" ] && ok "a dry run changed nothing" || bad "a dry run changed the compose file"

echo
echo "==> upgrading"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -16 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "upgraded (exit 0)" || bad "upgrade exit $rc"
ls $DIR/backups/server-*.tar.gz >/dev/null 2>&1 && ok "the server's data was archived: $(ls $DIR/backups)" || bad "no archive in $DIR/backups"
tar -tzf $DIR/backups/server-*.tar.gz 2>/dev/null | grep -q "config.json" && ok "with its configuration in it" || bad "the archive has no config.json"
AFTER="$(img server) $(img admin) $(img webmail)"
[ "$AFTER" != "$BEFORE" ] && ok "now: $AFTER" || bad "the images did not change"
STATE="$($(rt) compose -f $DIR/compose.yaml ps --format '{{.Service}} {{.State}}')"
for s in server console webmail; do grep -q "^$s running" <<<"$STATE" && ok "$s is running" || bad "$s is not running"; done
signs_in && ok "the same mailbox still signs in" || bad "the first mailbox no longer signs in"
curl -fsS http://127.0.0.1:8080/api/health | grep -q '"ok":true' && ok "the webmail is healthy" || bad "the webmail is not healthy"

echo
echo "==> again"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "Nothing to do" <<<"$OUT" && ok "a second upgrade has nothing to do" || bad "second upgrade: exit $rc"
/tmp/inbuxa status >/dev/null 2>&1 && ok "status: intent and reality agree" || bad "status disagrees"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
