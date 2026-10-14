#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The first case: on a machine with nothing installed on it, does the
# installer describe that machine correctly, and does it say which shapes it
# can take as it is, which it can take once --install-deps adds what is
# missing, and why -- with a reason someone could act on?
#
# A fresh Debian cloud image has no container runtime and no node, which is
# exactly the interesting case: every container shape and the webmail's host
# shape are offers with --install-deps, and the server's host shape works as
# the machine is. Getting this wrong in either direction is the difference
# between an installer that reads a machine and one that guesses.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/survey.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
has()  { grep -q -- "$2" <<<"$1" && ok "$3" || { bad "$3"; echo "----"; echo "$1" | sed 's/^/    /'; echo "----"; }; }
hasnt() { grep -q -- "$2" <<<"$1" && { bad "$3"; } || ok "$3"; }

echo "==> survey, on a machine with nothing on it"
S="$(/tmp/inbuxa survey)"
echo "$S" | sed 's/^/    /'

has "$S" "Debian GNU/Linux 13" "names the distribution"
has "$S" "init                   systemd" "sees systemd"
has "$S" "running as             root" "knows it is root"
has "$S" "container runtime      none: neither docker nor podman is installed" "sees no container runtime"
has "$S" "node                   not installed (a host webmail runs on inbuxa's own Node 26; --install-deps adds it)" "sees no node, and offers its own"
hasnt "$S" "too old" "never judges a system Node"
has "$S" "25     SMTP" "lists the mail ports"
hasnt "$S" "cannot tell without root" "as root, every port is a real answer"
has "$S" "container (with --install-deps: no container runtime" "containers offered with --install-deps, with the reason"
has "$S" "server     container (with --install-deps" "the server can be a container once docker is in"
has "$S" "host (with --install-deps: the webmail runs on the installer's own Node 26" "the webmail on the host is offered, not refused"

echo
echo "==> a plan that this machine cannot carry out"
OUT="$(/tmp/inbuxa install --domain example.test 2>&1)"; rc=$?
[ $rc -ne 0 ] && ok "refuses (exit $rc)" || bad "should have refused"
has "$OUT" "cannot install as asked" "says so plainly"
has "$OUT" "no container runtime: neither docker nor podman is installed" "and why"
has "$OUT" "Pass --install-deps to let it" "and what would fix it"

echo
echo "==> a plan it can: the server on the host, front ends elsewhere"
OUT="$(/tmp/inbuxa install --domain example.test --server host --console skip --webmail skip --dry-run 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "accepted (exit 0)" || bad "should have been accepted, exit $rc"
has "$OUT" "/etc/systemd/system/inbuxa-server.service" "names the unit it would write"
has "$OUT" "user: inbuxa" "names the user it would create"
has "$OUT" "Ports it will bind: 25, 465, 993, 995, 4190, 80, 443" "lists every port"
has "$OUT" "MX    10 mail.example.test." "writes the MX the domain needs"
has "$OUT" "_dmarc.example.test." "and DMARC"
hasnt "$OUT" "admin.example.test" "says nothing about front ends it was told to skip"

echo
echo "==> nothing was installed by any of that"
[ ! -e /etc/systemd/system/inbuxa-server.service ] && ok "no unit written" || bad "a unit appeared"
[ ! -e /var/lib/inbuxa ] && ok "no deployment directory" || bad "a directory appeared"
id inbuxa >/dev/null 2>&1 && bad "a user appeared" || ok "no user created"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
