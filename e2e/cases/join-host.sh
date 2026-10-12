#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa join with the front ends on the host: the console as files and the
# webmail under systemd, behind the nginx this machine already runs
# (--proxy snippets), for a server elsewhere. No container runtime is
# installed at all. Two machines are played by one, as in join.sh.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/join-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

SERVER_DIR=/var/lib/inbuxa
DIR=/var/lib/inbuxa-joined
D=example.test
SERVER=http://127.0.0.1:8081
if [ -x /tmp/inbuxa-server-test ]; then
  export INBUXA_SERVER_BINARY=/tmp/inbuxa-server-test
  # A debug build needs a deeper stack than a release one, as the server's
  # own test suites do (RUST_MIN_STACK in its CI). A drop-in made before the
  # unit exists applies when it does.
  mkdir -p /etc/systemd/system/inbuxa-server.service.d
  printf '[Service]\nEnvironment=RUST_MIN_STACK=16777216\n' > /etc/systemd/system/inbuxa-server.service.d/test-build.conf
fi

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
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }

echo "==> the server, on its own"
OUT="$(/tmp/inbuxa install --local --domain example.test --server host --console skip --webmail skip --yes 2>&1)"; rc=$?
if [ $rc -eq 0 ]; then ok "installed the server alone"; else
  bad "server install exit $rc"; echo "$OUT" | tail -15 | sed 's/^/    /'
  journalctl -u inbuxa-server -n 30 --no-pager -o cat | sed 's/^/    | /'
  echo; echo "==> $pass passed, $fail failed (stopped: no server to join)"; exit 1
fi
[ -n "${INBUXA_SERVER_BINARY:-}" ] && echo "    (server: $(/usr/local/bin/inbuxa-server --version 2>&1 | head -1), from the build given)"

echo
echo "==> an API key, as an administrator makes one in the console"
# The console is a browser; here the server takes the administrator's
# password for one restart, the way the installer's own setup does.
AUSER=$(awk '/^administrator/ {print $2}' "$SERVER_DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$SERVER_DIR/credentials.txt")
echo "INBUXA_HTTP_BASIC_AUTH=all" >> "$SERVER_DIR/server.env"
systemctl restart inbuxa-server; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" urn:inbuxa:jmap:registry "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"join test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
sed -i '/^INBUXA_HTTP_BASIC_AUTH=/d' "$SERVER_DIR/server.env"
systemctl restart inbuxa-server; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }

ACCOUNT=$(curl -s -H "Authorization: Bearer $KEY" $SERVER/jmap/session | grep -o '"primaryAccounts":{[^}]*}' | sed -n 's/^"primaryAccounts":{"[^"]*":"\([^"]*\)".*/\1/p')
[ -n "$ACCOUNT" ] || bad "no account id in the key's session"

# From here this is "another machine": no install recorded on it.
mv /etc/inbuxa/install.json /etc/inbuxa/install.server.json

echo
echo "==> joining, both front ends on the host, behind nginx"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx openssl >/dev/null 2>&1
OUT="$(echo "$KEY" | /tmp/inbuxa join $SERVER --key - --domain $D --console host --webmail host --proxy snippets \
  --dir $DIR --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -20 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "joined (exit 0)" || bad "exit $rc"
{ command -v docker || command -v podman; } >/dev/null 2>&1 && bad "a container runtime was installed" || ok "no container runtime was installed"
[ -f $DIR/compose.yaml ] && bad "there is a compose project" || ok "no compose project"
systemctl is-active --quiet inbuxa-webmail && ok "inbuxa-webmail is running" || bad "inbuxa-webmail is not running"
grep -q "name=\"api-base-url\" content=\"$SERVER\"" $DIR/console/index.html && ok "the console's files point at the server" || bad "the console's index.html"
grep -q "^MAIL_SERVER_URL=$SERVER$" $DIR/webmail.env && ok "the webmail is told the server's address" || bad "webmail.env: $(grep MAIL_SERVER_URL $DIR/webmail.env)"
grep -q "$KEY" <<<"$(cat $DIR/*.env $DIR/.env 2>/dev/null)" && bad "the API key was written down here" || ok "the API key was not kept"
[ -f $DIR/proxy/nginx-inbuxa.conf ] && ok "it wrote the nginx configuration" || bad "no $DIR/proxy/nginx-inbuxa.conf"
grep -q "the server lets the console call it" <<<"$OUT" && ok "it checked the console may call the server" || bad "no CORS check in the output"

echo
echo "==> the server knows the front ends"
FE=$(jmap "$KEY" urn:inbuxa:jmap '[["inbuxa:FrontEnds/get",{"accountId":"'"$ACCOUNT"'"},"0"]]')
grep -q "\"adminUrl\":\"https://admin.$D\"" <<<"$FE" && ok "the console's address" || bad "adminUrl: $FE"
grep -q "\"webmailUrl\":\"https://webmail.$D\"" <<<"$FE" && ok "the webmail's address" || bad "webmailUrl: $FE"
grep -q '"webmailClientSecretSet":true' <<<"$FE" && ok "and a webmail secret" || bad "no webmail secret: $FE"

echo
echo "==> behind nginx, from the configuration it wrote"
mkdir -p /etc/ssl/inbuxa
for n in admin.$D webmail.$D; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$n" \
    -keyout /etc/ssl/inbuxa/$n.key.pem -out /etc/ssl/inbuxa/$n.fullchain.pem 2>/dev/null
done
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
nginx -t >/dev/null 2>&1 && ok "nginx -t accepts it" || { bad "nginx -t"; nginx -t 2>&1 | sed 's/^/    /'; }
systemctl reload nginx; sleep 3
via() { curl -sk --resolve "$1:443:127.0.0.1" "${@:2}"; }
via admin.$D https://admin.$D/ | grep -q "content=\"$SERVER\"" && ok "the console is served from its files" || bad "the console is not served"
via webmail.$D https://webmail.$D/api/health | grep -q '"ok":true' && ok "the webmail is healthy through nginx" || bad "the webmail through nginx"

echo
echo "==> a person signs in to the joined webmail, through nginx"
USER=$(awk '/^first mailbox/ {print $3}' "$SERVER_DIR/credentials.txt")
PASS=$(awk '/^first mailbox/{getline; print $2}' "$SERVER_DIR/credentials.txt")
rm -f /tmp/jar
LOC=$(via webmail.$D -o /dev/null -c /tmp/jar -w '%{redirect_url}' "https://webmail.$D/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^$SERVER/" <<<"$LOC" && ok "the webmail sends sign-in to the server" || bad "sign-in went to '${LOC:-nowhere}'"
q() { sed -n "s/.*[?&]$1=\([^&]*\).*/\1/p" <<<"$LOC"; }
urldecode() { printf '%b' "${1//%/\\x}"; }
STATE=$(q state); CHALLENGE=$(q code_challenge); REDIRECT=$(urldecode "$(q redirect_uri)")
[ "$REDIRECT" = "https://webmail.$D/api/auth/callback" ] && ok "for its public address" || bad "redirect_uri $REDIRECT"
ANSWER=$(curl -s -X POST $SERVER/api/auth -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d "{\"type\":\"authCode\",\"accountName\":\"$USER\",\"accountSecret\":\"$PASS\",\"clientId\":\"ihasmail-inbuxa\",\"redirectUri\":\"$REDIRECT\",\"codeChallenge\":\"$CHALLENGE\",\"codeChallengeMethod\":\"S256\"}")
AUTHCODE=$(sed -n 's/.*"client_\{0,1\}[cC]ode":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
[ -n "$AUTHCODE" ] && ok "the server takes the joined webmail's redirect" || { bad "the server's sign-in answered"; head -c 200 <<<"$ANSWER" | sed 's/^/    /'; }
BACK=$(via webmail.$D -o /dev/null -b /tmp/jar -c /tmp/jar -w '%{redirect_url}' "$REDIRECT?code=$AUTHCODE&state=$STATE")
grep -q "signin_error" <<<"$BACK" && bad "the webmail's code exchange failed (its secret?): $BACK" || ok "the webmail exchanges the code with the secret made here"
CODE=$(via webmail.$D -o /tmp/login.json -b /tmp/jar -w '%{http_code}' -H 'X-Requested-With: ihasmail' https://webmail.$D/api/auth/session)
[ "$CODE" = 200 ] && grep -q "urn:ietf:params:jmap:mail" /tmp/login.json && ok "signed in, with mail" || { bad "the session answered $CODE"; head -c 200 /tmp/login.json | sed 's/^/    /'; }

echo
echo "==> status, and again"
STATUS="$(/tmp/inbuxa status 2>&1)"
for k in console webmail; do grep -q "$k.*installed as a host" <<<"$STATUS" && ok "status: the $k is a host install" || bad "status: $k"; done
OUT="$(INBUXA_JOIN_KEY=$KEY /tmp/inbuxa join $SERVER --domain $D --console host --webmail host --proxy snippets --dir $DIR --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "joining again converges" || { bad "second join exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
systemctl is-active --quiet inbuxa-webmail && ok "and the webmail is still running" || bad "the webmail stopped"

mv /etc/inbuxa/install.server.json /etc/inbuxa/install.json.server-kept 2>/dev/null
echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
