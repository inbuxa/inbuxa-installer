#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# What is here, as against what we wrote down that we did.
#
# The interesting cases are the ones where those two part company: a
# container stopped by hand, and a deployment with no state file at all --
# installed by an older version, or copied from another machine. An installer
# that reports intent as though it were reality is worse than one that
# reports nothing, because the operator believes it.
#
# The export has to survive both, because a design that starts from a file
# that does not describe the machine is a design of somewhere else.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/status-export.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
has()  { grep -q -- "$2" <<<"$1" && ok "$3" || { bad "$3"; echo "$1" | sed 's/^/    /'; }; }

DIR=/var/lib/inbuxa
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}

echo "==> before anything is installed"
OUT="$(/tmp/inbuxa status 2>&1)"; rc=$?
has "$OUT" "Nothing is installed" "status says so"
[ $rc -eq 0 ] && ok "and that is not an error" || bad "exit $rc"
OUT="$(/tmp/inbuxa export 2>&1)"; rc=$?
[ $rc -ne 0 ] && ok "export refuses (exit $rc)" || bad "export invented something"

echo
echo "==> install, then ask what is here"
/tmp/inbuxa install --local --domain example.test --install-deps --yes >/tmp/install.log 2>&1 || { bad "install failed"; tail -5 /tmp/install.log | sed 's/^/    /'; }
OUT="$(/tmp/inbuxa status 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "status is clean (exit 0)" || bad "exit $rc"
has "$OUT" "server   installed as a container running" "the server is installed and running"
has "$OUT" "webmail  installed as a container running" "so is the webmail"
has "$OUT" "example.test" "and it knows the domain"

echo
echo "==> a container stopped by hand"
compose stop webmail >/dev/null 2>&1
OUT="$(/tmp/inbuxa status 2>&1)"; rc=$?
has "$OUT" "webmail  installed as a container stopped" "status shows it stopped"
has "$OUT" "webmail is installed and not running" "and calls it out"
[ $rc -ne 0 ] && ok "and says so in its exit code ($rc)" || bad "a machine that disagrees with itself exited 0"
compose start webmail >/dev/null 2>&1

echo
echo "==> export describes this machine"
/tmp/inbuxa export -o /tmp/exported.json >/dev/null 2>&1 || bad "export failed"
python3 - <<'PY'
import json
t = json.load(open('/tmp/exported.json'))
m = t['machines'][0]
kinds = sorted(c['kind'] for c in m['components'])
print(f"    domain={t['domain']} machines={len(t['machines'])} components={','.join(kinds)} proxy={m.get('proxy','')}")
assert t['domain'] == 'example.test', t['domain']
assert kinds == ['console', 'server', 'webmail'], kinds
PY
[ $? -eq 0 ] && ok "the file says what is here" || bad "the exported file is wrong"

echo
echo "==> and the file it wrote is one it accepts back"
OUT="$(/tmp/inbuxa plan -f /tmp/exported.json 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "plan reads it (exit 0)" || { bad "exit $rc"; echo "$OUT" | sed 's/^/    /'; }
has "$OUT" "nothing to do" "and finds nothing to do, which is the point"

echo
echo "==> a deployment nobody wrote down: the state file removed"
cp /etc/inbuxa/install.json /tmp/state.json.bak
rm -f /etc/inbuxa/install.json
OUT="$(/tmp/inbuxa status 2>&1)"
echo "$OUT" | sed 's/^/    /'
has "$OUT" "in the deployment" "status describes it from the deployment itself"
has "$OUT" "nothing recorded installing it" "and says nothing recorded it"
/tmp/inbuxa export -o /tmp/exported2.json >/dev/null 2>&1 || bad "export failed without state"
python3 - <<'PY'
import json
t = json.load(open('/tmp/exported2.json'))
kinds = sorted(c['kind'] for c in t['machines'][0]['components'])
print(f"    rebuilt from the deployment: domain={t['domain']} components={','.join(kinds)}")
assert t['domain'] == 'example.test', t['domain']
assert kinds == ['console', 'server', 'webmail'], kinds
PY
[ $? -eq 0 ] && ok "export rebuilds the file from the deployment" || bad "export could not describe an unrecorded deployment"
cp /tmp/state.json.bak /etc/inbuxa/install.json

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
