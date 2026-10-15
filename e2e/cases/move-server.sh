#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Moving the mail server between a container and the host by running install
# again with the other --server: refused without a key and with a wrong one,
# changing nothing; put back when the host install fails before the move;
# then into the host and back into a container, each time keeping its
# accounts and mail, its front ends working, the old copy left in place and
# an archive taken first.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/move-server.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
SERVER=http://127.0.0.1:8081
INSTALL=(/tmp/inbuxa install --local --domain example.test --install-deps)

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
jmap() { # jmap TOKEN-or-user:pass CAPABILITY JSON-CALLS
  local auth
  case "$1" in *:*) auth=(-u "$1") ;; *) auth=(-H "Authorization: Bearer $1") ;; esac
  curl -s "${auth[@]}" -H 'Content-Type: application/json' \
    -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"$2\"],\"methodCalls\":$3}" "$SERVER/jmap/"
}
live() { [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ]; }
wait_server() { for _ in $(seq 1 60); do live && return 0; sleep 2; done; return 1; }
in_container() { compose ps --status running --services 2>/dev/null | grep -qx server; }
on_host() { systemctl is-active --quiet inbuxa-server; }

# The server keeps what it had: the account made before the move is still
# there, the first mailbox still signs in over IMAP, and the webmail and the
# console still answer.
kept() {
  local where=$1
  wait_server && ok "$where: the server answers on $SERVER" || bad "$where: the server does not answer"
  local got
  got=$(jmap "$KEY" urn:inbuxa:jmap:registry '[["x:Account/get",{"ids":null,"properties":["name"]},"0"]]')
  grep -q '"name":"movetest"' <<<"$got" && ok "$where: the account made before the move is there" || { bad "$where: movetest is gone"; head -c 300 <<<"$got" | sed 's/^/    /'; }
  # A --local container install publishes no mail ports, so IMAP is asked
  # from inside the server's own network there; on the host it is on loopback.
  local imap=(curl -sk --max-time 10 "imaps://127.0.0.1:993/" -u "$FIRST:$FIRSTPASS")
  if in_container; then
    imap=($(rt) run --rm --network "container:$(compose ps -q server)" docker.io/curlimages/curl:8.11.1 "${imap[@]:1}")
  fi
  "${imap[@]}" 2>/dev/null | grep -q INBOX \
    && ok "$where: $FIRST signs in over IMAP and sees its INBOX" || bad "$where: IMAP sign-in for $FIRST failed"
  curl -s --max-time 10 http://127.0.0.1:8080/api/health | grep -q '"ok":true' \
    && ok "$where: the webmail answers" || bad "$where: the webmail does not answer"
  curl -s --max-time 10 http://127.0.0.1:8082/ | grep -q api-base-url \
    && ok "$where: the console answers" || bad "$where: the console does not answer"
  /tmp/inbuxa status >/dev/null 2>&1 && ok "$where: status agrees with what runs" || bad "$where: status disagrees: $(/tmp/inbuxa status 2>&1 | tail -3)"
}

echo "==> all three in containers"
OUT="$("${INSTALL[@]}" --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed" || { bad "install exit $rc"; echo "$OUT" | tail -12 | sed 's/^/    /'; exit 1; }
FIRST=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
FIRSTPASS=$(awk '/^first mailbox/{getline; print $2}' "$DIR/credentials.txt")
[ -n "$FIRST" ] && [ -n "$FIRSTPASS" ] || bad "no first mailbox in credentials.txt"

echo
echo "==> an API key, as an administrator makes one in the console"
# The console is a browser; here the server takes the administrator's
# password for one restart, the way the installer's own setup does.
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
printf 'services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n' > /tmp/basic.yaml
compose -f /tmp/basic.yaml up -d server >/dev/null 2>&1; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" urn:inbuxa:jmap:registry "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"move test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
compose up -d server >/dev/null 2>&1; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; exit 1; }
DOMAIN_ID=$(jmap "$KEY" urn:inbuxa:jmap:registry '[["x:Domain/query",{},"0"]]' | sed -n 's/.*"ids":\["\([^"]*\)".*/\1/p')
MADE=$(jmap "$KEY" urn:inbuxa:jmap:registry "[[\"x:Account/set\",{\"create\":{\"u\":{\"@type\":\"User\",\"name\":\"movetest\",\"domainId\":\"$DOMAIN_ID\",\"description\":null,\"credentials\":{\"0\":{\"@type\":\"Password\",\"secret\":\"move-test-pass-4410\"}},\"roles\":{\"@type\":\"User\"},\"permissions\":{\"@type\":\"Inherit\"},\"quotas\":{},\"aliases\":{},\"memberGroupIds\":{},\"encryptionAtRest\":{\"@type\":\"Disabled\"}}}},\"0\"]]")
grep -q '"created"' <<<"$MADE" && ok "an account to look for after the move" || { bad "no movetest account"; head -c 300 <<<"$MADE" | sed 's/^/    /'; }
kept "in a container"

echo
echo "==> asked to move, without a key or with a wrong one: nothing changes"
OUT="$("${INSTALL[@]}" --server host --dry-run 2>&1)"
grep -q "Moving the mail server from a container install to a host install" <<<"$OUT" \
  && ok "the plan says it is a move" || bad "the plan says nothing of a move"
OUT="$(INBUXA_API_KEY= "${INSTALL[@]}" --server host --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "API key is needed" <<<"$OUT" && ok "without a key it stops and says one is needed" || { bad "no key: exit $rc"; echo "$OUT" | tail -4 | sed 's/^/    /'; }
in_container && ! on_host && live && ok "the server is still in its container" || bad "the server moved or stopped without a key"
OUT="$(INBUXA_API_KEY=wrong-key "${INSTALL[@]}" --server host --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "didn't take the API key" <<<"$OUT" && ok "a wrong key is refused, and says so" || { bad "wrong key: exit $rc"; echo "$OUT" | tail -4 | sed 's/^/    /'; }
in_container && ! on_host && live && ok "the server is still in its container" || bad "the server moved or stopped with a wrong key"

echo
echo "==> a host install that fails before the move puts the container back"
printf '#!/bin/sh\necho "inbuxa 9.9.9 (broken)"\nexit 1\n' > /tmp/broken-server; chmod +x /tmp/broken-server
OUT="$(echo "$KEY" | INBUXA_SERVER_BINARY=/tmp/broken-server "${INSTALL[@]}" --server host --key - --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && grep -q "putting it back in its container" <<<"$OUT" && ok "it stopped and put the server back" || { bad "broken binary: exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
in_container && ! on_host && ok "the server is in its container again" || bad "not back in its container"
grep -q '"server": "container"' /etc/inbuxa/install.json && ok "the install record still says container" || bad "the record changed: $(grep -o '"server": "[a-z]*"' /etc/inbuxa/install.json)"
kept "after the failed move"

echo
echo "==> into the host"
OUT="$(echo "$KEY" | "${INSTALL[@]}" --server host --key - --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | sed -n '/moving the mail server/,$p' | head -24 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "moved (exit 0)" || { bad "move exit $rc"; journalctl -u inbuxa-server -n 30 --no-pager -o cat | sed 's/^/    | /'; }
on_host && ok "inbuxa-server is running on the host" || bad "inbuxa-server is not running"
in_container && bad "a server container still runs" || ok "no server container runs"
grep -q '"path":"/var/lib/inbuxa/server/data/"' $DIR/server/config.json && ok "its configuration points at $DIR/server/data/" || bad "config.json: $(cat $DIR/server/config.json)"
[ "$(stat -c %U $DIR/server/data)" = inbuxa ] && ok "its data belongs to the inbuxa user" || bad "data owned by $(stat -c %U $DIR/server/data)"
ls $DIR/backups/server-*.tar.gz >/dev/null 2>&1 && ok "the volumes were archived first" || bad "no archive"
$(rt) volume inspect inbuxa_inbuxa-data >/dev/null 2>&1 && ok "the volumes are left in place" || bad "the volumes are gone"
[ ! -e /run/systemd/system/inbuxa-server.service.d/inbuxa-setup.conf ] && ok "no loopback fence is left behind" || bad "the setup drop-in is still there"
ss -ltn | grep -q '0.0.0.0:8080 \|\[::\]:8080 ' && bad "the server's HTTP is on every interface" || ok "the server's HTTP is not on every interface"
grep -qF "$KEY" <<<"$(cat $DIR/* $DIR/server/*.json /etc/inbuxa/install.json 2>/dev/null)" && bad "the API key was written down" || ok "the API key was not kept"
grep -q '"server": "host"' /etc/inbuxa/install.json && ok "the install record says host" || bad "the record: $(grep -o '"server": "[a-z]*"' /etc/inbuxa/install.json)"
kept "on the host"
OUT="$("${INSTALL[@]}" --server host --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ! grep -q "moving the mail server" <<<"$OUT" && ok "running it again is a converge, not another move" || bad "a second run: exit $rc"

echo
echo "==> back into a container"
OUT="$(echo "$KEY" | "${INSTALL[@]}" --server container --key - --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | sed -n '/moving the mail server/,$p' | head -24 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "moved back (exit 0)" || { bad "move back exit $rc"; compose logs --tail 30 server 2>&1 | sed 's/^/    | /'; }
in_container && ok "the server runs in its container" || bad "no server container"
on_host && bad "inbuxa-server still runs on the host" || ok "inbuxa-server is not running on the host"
[ ! -e /etc/systemd/system/inbuxa-server.service ] && [ ! -e /usr/local/bin/inbuxa-server ] \
  && ok "the unit and the binary are gone" || bad "the host install's unit or binary is left"
CFG=$($(rt) run --rm --entrypoint cat -v inbuxa_inbuxa-etc:/etc/inbuxa "$(compose config --images | grep inbuxa-server)" /etc/inbuxa/config.json 2>/dev/null)
grep -q '"path":"/var/lib/inbuxa/"' <<<"$CFG" && ok "its configuration points at /var/lib/inbuxa/ again" || bad "config.json in the volume: $CFG"
ls $DIR/backups/server-host-*.tar.gz >/dev/null 2>&1 && ok "the host copy was archived first" || bad "no archive of the host copy"
grep -q "earlier install" <<<"$OUT" && ok "the volumes' older data was archived, not mixed in" || bad "nothing said about the volumes' older data"
[ -d $DIR/server/data ] && ok "$DIR/server is left in place" || bad "$DIR/server is gone"
grep -q '"server": "container"' /etc/inbuxa/install.json && ok "the install record says container" || bad "the record: $(grep -o '"server": "[a-z]*"' /etc/inbuxa/install.json)"
kept "back in a container"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
