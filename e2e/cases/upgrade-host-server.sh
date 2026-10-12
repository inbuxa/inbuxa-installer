#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa upgrade, the server on the host: a binary that won't run is put back
# with the server answering as before, then the real release goes in, with
# the old binary kept beside it and the data archived first.
#
# Run from the host with:
#   OLD_REF=v2026.10.12.1 e2e/vm/run.sh e2e/cases/upgrade-host-server.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
DIR=/var/lib/inbuxa
BIN=/usr/local/bin/inbuxa-server
ver() { $BIN --version 2>/dev/null | awk '{print $1}'; }
live() { curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/.well-known/jmap | grep -qE '^(200|401|30[0-9])$'; }

echo "==> installed by the older installer, the server on the host"
OUT="$(/tmp/inbuxa-old install --local --domain example.test --server host --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed: server $(ver)" || { bad "old install exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
OLD=$(ver)

echo
echo "==> a release that won't start is put back"
printf '#!/bin/sh\necho "inbuxa 9.9.9 (broken)"\nexit 1\n' > /tmp/broken-server; chmod +x /tmp/broken-server
OUT="$(INBUXA_SERVER_BINARY=/tmp/broken-server /tmp/inbuxa upgrade server --no-backup --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "put back" <<<"$OUT" && ok "the upgrade stopped and said it put the server back" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
[ "$(ver)" = "$OLD" ] && ok "the binary is $OLD again" || bad "the binary is now $(ver)"
for _ in $(seq 1 30); do live && break; sleep 2; done
live && ok "and the server answers" || bad "the server is down after the rollback"

echo
echo "==> the real release"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -10 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "upgraded (exit 0)" || bad "upgrade exit $rc"
[ "$(ver)" != "$OLD" ] && ok "the server is $(ver) now" || bad "still $OLD"
[ -x $BIN.$OLD ] && ok "the old binary is kept as $BIN.$OLD" || bad "no $BIN.$OLD"
ls $DIR/backups/server-$OLD-*.tar.gz >/dev/null 2>&1 && ok "the data was archived first" || bad "no archive"
live && ok "the server answers" || bad "the server does not answer"
systemctl is-active --quiet inbuxa-server && ok "inbuxa-server is running" || bad "not running"
OUT="$(/tmp/inbuxa upgrade --yes 2>&1)"
grep -q "Nothing to do" <<<"$OUT" && ok "a second upgrade has nothing to do" || bad "a second upgrade did something"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
