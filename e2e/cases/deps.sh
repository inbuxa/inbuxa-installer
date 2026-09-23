#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The offer: when a shape needs something this machine has not got, does the
# installer say what it would do, and then actually do it?
#
# Starts on a machine with neither Docker nor Node, which is the case worth
# proving: refusing there is easy and useless. By the end, containers work
# and a host install of the webmail has a Node to run on -- all of it fetched
# against pinned checksums.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/deps.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
has()  { grep -q -- "$2" <<<"$1" && ok "$3" || { bad "$3"; echo "$1" | sed 's/^/    /'; }; }

echo "==> a machine with nothing on it is told what is missing"
OUT="$(/tmp/inbuxa deps --console skip --webmail skip 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "saying so is not an error (exit 0)" || bad "exit $rc"
has "$OUT" "docker is missing" "names docker"
has "$OUT" "compose is missing" "names the compose plugin"
has "$OUT" "from the distribution's own archive" "says where docker comes from"
has "$OUT" "pinned checksum" "says the download is checked"
has "$OUT" "Pass --install to do it" "offers to do it"
command -v docker >/dev/null && bad "docker appeared without being asked for" || ok "nothing installed yet"

echo
echo "==> the refusal to install carries the same offer"
OUT="$(/tmp/inbuxa install --domain example.test --console skip --webmail skip 2>&1)"; rc=$?
[ $rc -ne 0 ] && ok "still refuses to install (exit $rc)" || bad "should have refused"
has "$OUT" "The installer can fix that" "but offers the fix"
has "$OUT" "Pass --install-deps" "and says how to accept"

echo
echo "==> the plan, once the offer is accepted, has a step for it"
OUT="$(/tmp/inbuxa install --domain example.test --console skip --webmail skip --install-deps --dry-run 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "accepted (exit 0)" || { bad "exit $rc"; echo "$OUT" | sed 's/^/    /'; }
has "$OUT" "Install what this machine is missing" "the first step is the fix"
has "$OUT" "docker: install docker.io" "which names the package"
has "$OUT" "Start the mail server as a container" "and the install goes on as a container install"

echo
echo "==> doing it"
OUT="$(/tmp/inbuxa deps --console skip --webmail skip --install 2>&1)"; rc=$?
echo "$OUT" | tail -20 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
has "$OUT" "checksum ok" "checked what it downloaded"
has "$OUT" "docker                 usable" "and says the machine can do containers now"

docker version >/dev/null 2>&1 && ok "docker answers" || bad "docker does not answer"
docker compose version >/dev/null 2>&1 && ok "compose v2 answers" || bad "compose does not answer"
systemctl is-enabled docker >/dev/null 2>&1 && ok "docker is enabled at boot" || bad "docker is not enabled"

echo
echo "==> node, for a host install of the webmail"
OUT="$(/tmp/inbuxa deps --server skip --console skip --webmail host --install 2>&1)"; rc=$?
echo "$OUT" | head -12 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
has "$OUT" "checksum ok" "checked the tarball"
V="$(/opt/inbuxa/node/bin/node --version 2>/dev/null)"
[[ "$V" == v22.* ]] && ok "node $V is in /opt/inbuxa/node" || bad "no usable node in /opt/inbuxa/node (got '$V')"
command -v node >/dev/null && bad "it put node on PATH; it should stay out of the way" || ok "it stayed out of PATH"

echo
echo "==> and now nothing is missing"
OUT="$(/tmp/inbuxa deps --server container --console container --webmail host 2>&1)"
has "$OUT" "Nothing is missing" "says so"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
