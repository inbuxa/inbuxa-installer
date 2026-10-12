#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa upgrade, the console and the webmail on the host: installed from
# older tarballs, moved to this installer's releases, the webmail answering
# as the new version afterwards and the console still pointed at the server.
#
# Run from the host with older tarballs:
#   CONSOLE_TARBALL=… WEBMAIL_TARBALL=… e2e/vm/run.sh e2e/cases/upgrade-frontends-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
DIR=/var/lib/inbuxa

echo "==> installed from older tarballs"
OUT="$(INBUXA_CONSOLE_TARBALL=/tmp/inbuxa-console-test.tar.gz INBUXA_WEBMAIL_TARBALL=/tmp/inbuxa-webmail-test.tar.gz \
  /tmp/inbuxa install --domain example.test --console host --webmail host --proxy snippets --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
OLDW=$(cat $DIR/webmail/VERSION)
echo "    webmail $OLDW, console $(awk '{print $2}' $DIR/console/.inbuxa-release)"

echo
echo "==> upgrading"
DRY="$(/tmp/inbuxa upgrade --dry-run 2>&1)"
grep -q "console.*local -> " <<<"$DRY" && grep -q "webmail.*-> " <<<"$DRY" && ok "the dry run shows both to move" || { bad "dry run"; echo "$DRY" | sed 's/^/    /'; }
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -10 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "upgraded (exit 0)" || bad "upgrade exit $rc"
NEWW=$(cat $DIR/webmail/VERSION)
[ "$NEWW" != "$OLDW" ] && ok "the webmail's files are $NEWW" || bad "the webmail's files are still $OLDW"
curl -fsS http://127.0.0.1:8080/api/health | grep -q "\"version\":\"$NEWW\"" && ok "and it answers as $NEWW" || bad "it doesn't answer as $NEWW"
grep -q "^IHASMAIL_VERSION=$NEWW$" $DIR/webmail.env && ok "its environment says so" || bad "webmail.env still says $(grep IHASMAIL_VERSION $DIR/webmail.env)"
[ "$(stat -c %U $DIR/webmail/server/dist/index.js)" = root ] && ok "its files are still root's" || bad "owner changed"
grep -q 'name="api-base-url" content="https://mail.example.test"' $DIR/console/index.html && ok "the console is still pointed at the server" || bad "the console lost the server's address"
[ "$(awk '{print $2}' $DIR/console/.inbuxa-release)" != local ] && ok "the console is release $(awk '{print $2}' $DIR/console/.inbuxa-release)" || bad "the console is still the local tarball"
[ ! -e $DIR/console.prev ] && [ ! -e $DIR/webmail.prev ] && ok "no previous copies left behind" || bad "a .prev directory was left"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
