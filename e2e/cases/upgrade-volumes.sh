#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# An install made before issue #5 keeps the server's configuration and mail
# in anonymous volumes, because its compose file mounted the named ones where
# the server never looks. This installs with that installer, upgrades with
# this one, and proves nothing was lost: on the upgrade, after a `down` and
# `up` (which used to start the server empty), and on a run after that.
#
# Run from the host with:
#   OLD_REF=2fce971 e2e/vm/run.sh e2e/cases/upgrade-volumes.sh
# where OLD_REF is any commit from before the fix.
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

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
mounts() { "$(rt)" inspect --format '{{range .Mounts}}{{.Name}} {{.Destination}}{{println}}{{end}}' "$(compose ps -q server)"; }
wait_live() {
  for _ in $(seq 1 60); do curl -fsS -o /dev/null http://127.0.0.1:8081/healthz/live 2>/dev/null && return 0; sleep 2; done
  return 1
}
# Whether the server still knows the first mailbox and its password: what
# the server's own sign-in page does, as the webmail's registered client.
signs_in() {
  local user pass
  user=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
  pass=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
  curl -s -X POST http://127.0.0.1:8081/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"type\":\"authCode\",\"accountName\":\"$user\",\"accountSecret\":\"$pass\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"http://127.0.0.1:8080/api/auth/callback\",\"codeChallenge\":\"$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')\",\"codeChallengeMethod\":\"S256\"}" \
    | grep -q '"type":"authenticated"'
}

[ -x /tmp/inbuxa-old ] || { echo "no /tmp/inbuxa-old: run with OLD_REF=<commit before the fix>" >&2; exit 2; }

echo "==> installing with the installer from before the fix"
OUT="$(/tmp/inbuxa-old install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; echo "$OUT" | grep -v '^    |' | tail -15 | sed 's/^/    /'; }
signs_in && ok "the first mailbox signs in" || bad "the first mailbox does not sign in"
M="$(mounts)"
grep -q "inbuxa_inbuxa-data /opt/stalwart/data" <<<"$M" && ok "it has the old layout: the named volume is at /opt/stalwart/data" \
  || { bad "not the old layout; this case proves nothing on it"; echo "$M" | sed 's/^/    /'; }
OLD_DATA=$(awk '$2 == "/var/lib/inbuxa" {print $1}' <<<"$M")

echo
echo "==> upgrading with this installer"
OUT="$(/tmp/inbuxa install --local --domain example.test --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "upgraded (exit 0)" || { bad "exit $rc"; echo "$OUT" | grep -v '^    |' | tail -15 | sed 's/^/    /'; }
grep -q "moving the mail server's data onto its named volumes" <<<"$OUT" && ok "it moved the data" || bad "it did not say it moved the data"
grep -q "came back with its configuration" <<<"$OUT" && ok "and checked the server came back configured" || bad "no check that the server came back configured"
M="$(mounts)"
grep -q "inbuxa_inbuxa-etc /etc/inbuxa" <<<"$M" && grep -q "inbuxa_inbuxa-data /var/lib/inbuxa" <<<"$M" \
  && ok "the server's data is on its named volumes" || { bad "the mounts are not the named volumes"; echo "$M" | sed 's/^/    /'; }
signs_in && ok "the first mailbox still signs in" || bad "the first mailbox no longer signs in"
"$(rt)" volume inspect "$OLD_DATA" >/dev/null 2>&1 && ok "the old volume is kept, for undoing it" || bad "the old volume $OLD_DATA is gone"

echo
echo "==> down and up, which used to start the server empty"
compose down >/dev/null 2>&1 && compose up -d >/dev/null 2>&1 && wait_live && ok "the stack came back" || bad "the stack did not come back"
compose logs server 2>&1 | grep -q "bootstrap mode" && bad "the server came back in bootstrap mode" || ok "not in bootstrap mode"
signs_in && ok "the first mailbox still signs in" || bad "the first mailbox no longer signs in"

echo
echo "==> another run has nothing to move"
OUT="$(/tmp/inbuxa install --local --domain example.test --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "ran again (exit 0)" || bad "exit $rc"
grep -q "moving the mail server's data" <<<"$OUT" && bad "it moved the data again" || ok "it moved nothing"
signs_in && ok "the first mailbox still signs in" || bad "the first mailbox no longer signs in"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
