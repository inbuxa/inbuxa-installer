# --proxy snippets on a machine that already runs nginx: the installer takes
# neither 80 nor 443, writes the configuration for nginx, and the suite is
# then reached through nginx on its real names once that file is loaded.
# nginx routes 443 by name (#48): it terminates the console and the webmail,
# with the operator's certificates (self-signed here), and passes every other
# name through to the mail server's own HTTPS, a domain added later included.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/proxy-snippets.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test
SERVER=http://127.0.0.1:8081

echo "==> a machine with nginx already on it"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx libnginx-mod-stream openssl >/dev/null 2>&1
systemctl is-active --quiet nginx && ok "nginx is running, holding 80" || { bad "no nginx"; echo "==> $pass passed, $fail failed"; exit 1; }

echo
echo "==> install behind it"
OUT="$(/tmp/inbuxa install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -14 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
grep -q "proxy config.*$DIR/proxy" <<<"$OUT" && ok "the summary names the proxy configuration" || bad "no proxy config in the summary"
[ -f $DIR/proxy/nginx-inbuxa.conf ] && [ -f $DIR/proxy/nginx-inbuxa-stream.conf ] && [ -f $DIR/proxy/Caddyfile.fragment ] \
  && ok "nginx-inbuxa.conf, nginx-inbuxa-stream.conf and Caddyfile.fragment written" || bad "the configuration files are missing"
grep -q '"passThrough": true' /etc/inbuxa/install.json && ok "the install record says the server's names pass through" || bad "no passThrough in the install record"
grep -q '"127.0.0.1:8443:443"' $DIR/compose.yaml && ok "the server's own HTTPS is published on 127.0.0.1:8443" || bad "8443 is not published"
grep -q "caddy" $DIR/compose.yaml && bad "a Caddy was put here anyway" || ok "no Caddy of its own"
systemctl is-active --quiet nginx && ok "nginx was left running, untouched" || bad "nginx is not running any more"

echo
echo "==> load it, as the operator would"
mkdir -p /etc/ssl/inbuxa
# Only the front ends' certificates: the server's names are its own.
for n in admin.$D webmail.$D; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$n" \
    -addext "subjectAltName=DNS:$n" \
    -keyout /etc/ssl/inbuxa/$n.key.pem -out /etc/ssl/inbuxa/$n.fullchain.pem 2>/dev/null
done
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
echo "include $DIR/proxy/nginx-inbuxa-stream.conf;" >> /etc/nginx/nginx.conf
OUT="$(nginx -t 2>&1)" && ok "nginx -t accepts it" || { bad "nginx -t"; echo "$OUT" | sed 's/^/    /'; }
systemctl reload nginx
# A reload returns before nginx's old workers have finished; until they
# have, port 80 can still be answered from the configuration before it.
sleep 3

echo
echo "==> the suite, through nginx"
via() { curl -sk --resolve "$1:443:127.0.0.1" --resolve "$1:80:127.0.0.1" "${@:2}"; }
fp() { echo | timeout 10 openssl s_client -connect "${2:-127.0.0.1:443}" -servername "$1" 2>/dev/null | openssl x509 -noout -fingerprint -sha256 2>/dev/null; }
CODE=$(via mail.$D -o /dev/null -w '%{http_code}' https://mail.$D/.well-known/jmap)
case "$CODE" in 30*|200|401) ok "the mail server answers on https://mail.$D ($CODE)" ;; *) bad "https://mail.$D/.well-known/jmap answered $CODE" ;; esac
via admin.$D https://admin.$D/ | grep -q "content=\"https://mail.$D\"" && ok "the console, pointed at https://mail.$D" || bad "the console is not served through nginx"
via webmail.$D https://webmail.$D/api/health | grep -q '"ok":true' && ok "the webmail is healthy through nginx" || bad "the webmail is not healthy through nginx"
ADMIN_FP=$(openssl x509 -in /etc/ssl/inbuxa/admin.$D.fullchain.pem -noout -fingerprint -sha256)
WEBMAIL_FP=$(openssl x509 -in /etc/ssl/inbuxa/webmail.$D.fullchain.pem -noout -fingerprint -sha256)
[ "$(fp admin.$D)" = "$ADMIN_FP" ] && ok "the console is served with nginx's certificate" || bad "the console's certificate is not nginx's"
MAIL_FP=$(fp mail.$D)
if [ -n "$MAIL_FP" ] && [ "$MAIL_FP" != "$ADMIN_FP" ] && [ "$MAIL_FP" != "$WEBMAIL_FP" ]; then
  ok "mail.$D is passed through to the server's own certificate"
else
  bad "mail.$D is served '${MAIL_FP:-nothing}'"
fi
# The server takes the PROXY protocol header from nginx, and so refuses a
# connection from there without one.
fp mail.$D 127.0.0.1:8443 | grep -q Fingerprint && bad "the server's HTTPS answers without the PROXY header" || ok "the server's HTTPS takes nothing without nginx's PROXY header"
# The mail server's own 404 is a JSON problem document; nginx's is a page.
BODY=$(via mail.$D http://mail.$D/.well-known/acme-challenge/probe)
grep -q "<html" <<<"$BODY" && { bad "port 80 answered the challenge path itself, not the mail server"; echo "$BODY" | head -5 | sed 's/^/    /'; } || ok "port 80 passes the server's challenges to it"
CODE=$(via admin.$D -o /dev/null -w '%{http_code}' http://admin.$D/)
if [ "$CODE" = 308 ]; then ok "port 80 redirects the console to https"; else
  bad "http://admin.$D answered $CODE"
  via admin.$D -sv http://admin.$D/ 2>&1 | grep -E "^[<>] |^\* Connected" | head -12 | sed 's/^/    /'
  nginx -T 2>/dev/null | grep -n -E "^# configuration file|listen|server_name" | head -40 | sed 's/^/    /'
fi

echo
echo "==> a domain added later"
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
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
printf 'services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n' > /tmp/basic.yaml
compose -f /tmp/basic.yaml up -d server >/dev/null 2>&1; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(jmap "$AUSER:$APASS" "[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"snippets test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]")
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
compose up -d server >/dev/null 2>&1; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }
MADE=$(jmap "$KEY" '[["x:Domain/set",{"create":{"d":{"name":"second.test"}}},"0"]]')
grep -q '"created"' <<<"$MADE" && ok "second.test added on the server, not in nginx" || { bad "no second.test"; head -c 300 <<<"$MADE" | sed 's/^/    /'; }
sleep 2
LATER_FP=$(fp mta-sts.second.test)
if [ -n "$LATER_FP" ] && [ "$LATER_FP" != "$ADMIN_FP" ] && [ "$LATER_FP" != "$WEBMAIL_FP" ]; then
  ok "mta-sts.second.test reaches the server's own certificate through nginx"
else
  bad "mta-sts.second.test is served '${LATER_FP:-nothing}'"
fi
CODE=$(via mta-sts.second.test -o /dev/null -w '%{http_code}' https://mta-sts.second.test/.well-known/mta-sts.txt)
[ "$CODE" != 000 ] && ok "and the server answers HTTPS for it ($CODE)" || bad "no HTTPS answer for mta-sts.second.test"
BODY=$(via mta-sts.second.test http://mta-sts.second.test/.well-known/acme-challenge/probe)
grep -q "<html" <<<"$BODY" && bad "port 80 kept mta-sts.second.test's challenges from the server" || ok "port 80 passes its challenges to the server"

echo
echo "==> again: converges, and rewrites the file for what is here"
rm $DIR/proxy/nginx-inbuxa.conf $DIR/proxy/nginx-inbuxa-stream.conf
OUT="$(INBUXA_API_KEY= /tmp/inbuxa install --domain $D --proxy snippets --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && [ -f $DIR/proxy/nginx-inbuxa.conf ] && [ -f $DIR/proxy/nginx-inbuxa-stream.conf ] \
  && ok "a second run converges and writes both again, without a key" || { bad "second run exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
[ "$(fp mail.$D)" = "$MAIL_FP" ] && ok "mail.$D still passes through" || bad "mail.$D after the second run"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
