#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# A --proxy snippets install from before nginx routed 443 by name (#48): run
# again without a key it keeps the layout it has, since a new file in front of
# a server not fitted for it would break its names; with a key it moves over.
#
# Run from the host with: OLD_REF=<a commit before #48> e2e/vm/run.sh e2e/cases/proxy-snippets-upgrade.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test
SERVER=http://127.0.0.1:8081
[ -x /tmp/inbuxa-old ] || { echo "needs OLD_REF: the installer before #48"; exit 1; }

rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
jmap() { # jmap TOKEN-or-user:pass JSON-CALLS
  local auth
  case "$1" in *:*) auth=(-u "$1") ;; *) auth=(-H "Authorization: Bearer $1") ;; esac
  curl -s "${auth[@]}" -H 'Content-Type: application/json' \
    -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"urn:inbuxa:jmap:registry\"],\"methodCalls\":$2}" "$SERVER/jmap/"
}
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }
via() { curl -sk --resolve "$1:443:127.0.0.1" "${@:2}"; }
fp() { echo | timeout 10 openssl s_client -connect 127.0.0.1:443 -servername "$1" 2>/dev/null | openssl x509 -noout -fingerprint -sha256 2>/dev/null; }
reload() { OUT="$(nginx -t 2>&1)" && ok "nginx -t accepts it" || { bad "nginx -t"; echo "$OUT" | sed 's/^/    /'; }; systemctl reload nginx; sleep 3; }

echo "==> installed with the installer before #48, behind nginx"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx libnginx-mod-stream openssl >/dev/null 2>&1
OUT="$(/tmp/inbuxa-old install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
mkdir -p /etc/ssl/inbuxa
for n in mail.$D admin.$D webmail.$D; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$n" \
    -addext "subjectAltName=DNS:$n,DNS:autoconfig.$D,DNS:autodiscover.$D,DNS:mta-sts.$D,DNS:ua-auto-config.$D" \
    -keyout /etc/ssl/inbuxa/$n.key.pem -out /etc/ssl/inbuxa/$n.fullchain.pem 2>/dev/null
done
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
reload
NGINX_MAIL_FP=$(openssl x509 -in /etc/ssl/inbuxa/mail.$D.fullchain.pem -noout -fingerprint -sha256)
[ "$(fp mail.$D)" = "$NGINX_MAIL_FP" ] && ok "mail.$D is served nginx's certificate, as before" || bad "mail.$D before: $(fp mail.$D)"

echo
echo "==> run again with this installer, without a key: nothing moves"
OUT="$(INBUXA_API_KEY= /tmp/inbuxa install --domain $D --proxy snippets --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "converged (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
grep -q "run this again with --key" <<<"$OUT" && ok "it says how to move over" || bad "no word about --key"
[ -f $DIR/proxy/nginx-inbuxa-stream.conf ] && bad "a name router was written for a server not fitted for it" || ok "no name router written"
grep -q "listen 443 ssl;" $DIR/proxy/nginx-inbuxa.conf && grep -q -- "--key" $DIR/proxy/nginx-inbuxa.conf \
  && ok "nginx-inbuxa.conf keeps its layout, and says how to move" || bad "nginx-inbuxa.conf changed"
grep -q ':443"' $DIR/compose.yaml && bad "the server's HTTPS was published anyway" || ok "compose.yaml publishes no HTTPS"
grep -q '"passThrough"' /etc/inbuxa/install.json && bad "the install record says passThrough" || ok "the install record doesn't say passThrough"
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
reload
[ "$(fp mail.$D)" = "$NGINX_MAIL_FP" ] && ok "mail.$D still works as it did" || bad "mail.$D after a run without a key: $(fp mail.$D)"

echo
echo "==> an API key, as an administrator makes one in the console"
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
printf 'services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n' > /tmp/basic.yaml
compose -f /tmp/basic.yaml up -d server >/dev/null 2>&1; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"snippets upgrade test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
compose up -d server >/dev/null 2>&1; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }

echo
echo "==> with the key: moved over"
OUT="$(echo "$KEY" | /tmp/inbuxa install --domain $D --proxy snippets --key - --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "converged (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -8 | sed 's/^/    /'; }
grep -q "fitting the server's HTTPS" <<<"$OUT" && ok "it fitted the server's HTTPS listener" || bad "no listener step"
[ -f $DIR/proxy/nginx-inbuxa-stream.conf ] && ok "the name router is written" || bad "no nginx-inbuxa-stream.conf"
grep -q '"127.0.0.1:8443:443"' $DIR/compose.yaml && ok "the server's HTTPS is published on 127.0.0.1:8443" || bad "8443 is not published"
grep -q '"passThrough": true' /etc/inbuxa/install.json && ok "the install record says passThrough" || bad "no passThrough in the install record"
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
echo "include $DIR/proxy/nginx-inbuxa-stream.conf;" >> /etc/nginx/nginx.conf
reload
MAIL_FP=$(fp mail.$D)
[ -n "$MAIL_FP" ] && [ "$MAIL_FP" != "$NGINX_MAIL_FP" ] && ok "mail.$D is now the server's own certificate" || bad "mail.$D after the move: '${MAIL_FP:-nothing}'"
CODE=$(via mail.$D -o /dev/null -w '%{http_code}' https://mail.$D/.well-known/jmap)
case "$CODE" in 30*|200|401) ok "and the server answers through it ($CODE)" ;; *) bad "https://mail.$D answered $CODE" ;; esac
via admin.$D https://admin.$D/ | grep -q "content=\"https://mail.$D\"" && ok "the console still through nginx" || bad "the console"
via webmail.$D https://webmail.$D/api/health | grep -q '"ok":true' && ok "the webmail still through nginx" || bad "the webmail"

echo
echo "==> again, with no key: stays moved"
OUT="$(INBUXA_API_KEY= /tmp/inbuxa install --domain $D --proxy snippets --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && [ -f $DIR/proxy/nginx-inbuxa-stream.conf ] && ok "converged, router kept" || { bad "exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
[ "$(fp mail.$D)" = "$MAIL_FP" ] && ok "mail.$D still the server's" || bad "mail.$D changed"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
