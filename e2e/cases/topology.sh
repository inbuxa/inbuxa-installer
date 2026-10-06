#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The topology file, and the half of it that can lose something: shrinking.
#
# Growing is easy to believe. What has to be proved is that taking a
# component out of the file takes it off the machine, leaves everything else
# working, and does not quietly delete anyone's mail on the way. And that a
# plan against a file this machine already matches says so rather than
# inventing work.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/topology.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }
has()  { grep -q -- "$2" <<<"$1" && ok "$3" || { bad "$3"; echo "$1" | sed 's/^/    /'; }; }

DIR=/var/lib/inbuxa
F=/tmp/topology.json

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}

cat > "$F" <<'JSON'
{
  "version": 1,
  "domain": "example.test",
  "machines": [
    {
      "name": "one",
      "components": [
        { "kind": "server",  "shape": "container" },
        { "kind": "console", "shape": "container" },
        { "kind": "webmail", "shape": "container" }
      ],
      "proxy": "none"
    }
  ]
}
JSON

echo "==> a file this machine has never seen"
OUT="$(/tmp/inbuxa plan -f "$F" --machine one 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "planning changes nothing (exit 0)" || bad "exit $rc"
has "$OUT" "add     server" "says it would add the server"
has "$OUT" "add     webmail" "and the webmail"
[ ! -e /etc/inbuxa/install.json ] && ok "and nothing was recorded" || bad "state appeared from a plan"

echo
echo "==> applying it"
OUT="$(/tmp/inbuxa apply -f "$F" --machine one --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -6 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "applied (exit 0)" || bad "exit $rc"
for s in server console webmail; do
  compose ps --format '{{.Service}} {{.State}}' | grep -q "^$s running" && ok "$s is running" || bad "$s is not running"
done
grep -q '"webmail": "container"' /etc/inbuxa/install.json && ok "the state file records what it installed" || { bad "state does not record the webmail"; cat /etc/inbuxa/install.json | sed 's/^/    /'; }
grep -q '"machine": "one"' /etc/inbuxa/install.json && ok "and which machine this is" || bad "state does not name the machine"

echo
echo "==> planning the same file again"
OUT="$(/tmp/inbuxa plan -f "$F" --machine one 2>&1)"
has "$OUT" "nothing to do" "says there is nothing to do"

echo
echo "==> now take the webmail out of the file"
python3 - "$F" <<'PY'
import json, sys
t = json.load(open(sys.argv[1]))
m = t["machines"][0]
m["components"] = [c for c in m["components"] if c["kind"] != "webmail"]
json.dump(t, open(sys.argv[1], "w"), indent=2)
PY
OUT="$(/tmp/inbuxa plan -f "$F" --machine one 2>&1)"
echo "$OUT" | sed 's/^/    /'
has "$OUT" "remove  webmail" "the plan says it would remove the webmail"
has "$OUT" "keep    server" "and keep the server"
compose ps --format '{{.Service}}' | grep -q webmail && ok "which has not happened yet" || bad "the webmail went away during a plan"

echo
echo "==> shrinking"
OUT="$(/tmp/inbuxa apply -f "$F" --machine one --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -8 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "applied (exit 0)" || bad "exit $rc"
compose ps --format '{{.Service}}' | grep -q webmail && bad "the webmail is still running" || ok "the webmail is gone"
curl -fsS -o /dev/null http://127.0.0.1:8080/api/health 2>/dev/null && bad "something still answers on the webmail's port" || ok "and nothing answers on its port"
grep -q '"webmail"' /etc/inbuxa/install.json && bad "state still claims the webmail" || ok "the state file agrees"

echo
echo "==> and what is left still works"
for s in server console; do
  compose ps --format '{{.Service}} {{.State}}' | grep -q "^$s running" && ok "$s is still running" || bad "$s is not running"
done
curl -fsS http://127.0.0.1:8082/ 2>/dev/null | grep -q 'api-base-url' && ok "the console still answers" || bad "the console does not answer"
ADMIN=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
ADMINPW=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
answered=
for _ in $(seq 1 30); do
  curl -fsS -u "$ADMIN:$ADMINPW" http://127.0.0.1:8081/.well-known/jmap >/dev/null 2>&1 && { answered=1; break; }
  sleep 2
done
[ -n "$answered" ] && ok "the mail server still answers for its administrator" || bad "the mail server does not answer"
$(rt) volume ls --format '{{.Name}}' | grep -q inbuxa-data && ok "the mail is still there: the data volume was not removed" || bad "the data volume is gone"

echo
echo "==> growing it back"
python3 - "$F" <<'PY'
import json, sys
t = json.load(open(sys.argv[1]))
t["machines"][0]["components"].append({"kind": "webmail", "shape": "container"})
json.dump(t, open(sys.argv[1], "w"), indent=2)
PY
OUT="$(/tmp/inbuxa apply -f "$F" --machine one --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "applied (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
compose ps --format '{{.Service}} {{.State}}' | grep -q "^webmail running" && ok "the webmail is back" || bad "the webmail did not come back"
curl -fsS http://127.0.0.1:8080/api/health 2>/dev/null | grep -q '"ok":true' && ok "and it is healthy" || bad "it is not healthy"

echo
echo "==> a file that describes an installation this cannot build"
cat > /tmp/bad.json <<'JSON'
{
  "version": 1,
  "domain": "example.test",
  "machines": [
    { "name": "a", "components": [ { "kind": "server", "shape": "container" } ] },
    { "name": "b", "components": [ { "kind": "server", "shape": "container" } ] }
  ]
}
JSON
OUT="$(/tmp/inbuxa plan -f /tmp/bad.json --machine a 2>&1)"; rc=$?
[ $rc -ne 0 ] && ok "two mail servers is refused (exit $rc)" || bad "two mail servers was accepted"
has "$OUT" "shared store" "and the refusal says why"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
