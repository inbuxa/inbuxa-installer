#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# --proxy snippets on a machine that already runs nginx: the installer takes
# neither 80 nor 443, writes the configuration for nginx, and the suite is
# then reached through nginx on its real names once that file is loaded --
# certificates being the operator's, self-signed here.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/proxy-snippets.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test

echo "==> a machine with nginx already on it"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx openssl >/dev/null 2>&1
systemctl is-active --quiet nginx && ok "nginx is running, holding 80" || { bad "no nginx"; echo "==> $pass passed, $fail failed"; exit 1; }

echo
echo "==> install behind it"
OUT="$(/tmp/inbuxa install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -14 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
grep -q "proxy config.*$DIR/proxy" <<<"$OUT" && ok "the summary names the proxy configuration" || bad "no proxy config in the summary"
[ -f $DIR/proxy/nginx-inbuxa.conf ] && [ -f $DIR/proxy/Caddyfile.fragment ] && ok "nginx-inbuxa.conf and Caddyfile.fragment written" || bad "the configuration files are missing"
grep -q "caddy" $DIR/compose.yaml && bad "a Caddy was put here anyway" || ok "no Caddy of its own"
systemctl is-active --quiet nginx && ok "nginx was left running, untouched" || bad "nginx is not running any more"

echo
echo "==> load it, as the operator would"
mkdir -p /etc/ssl/inbuxa
for n in mail.$D admin.$D webmail.$D; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$n" \
    -addext "subjectAltName=DNS:$n,DNS:autoconfig.$D,DNS:autodiscover.$D,DNS:mta-sts.$D,DNS:ua-auto-config.$D" \
    -keyout /etc/ssl/inbuxa/$n.key.pem -out /etc/ssl/inbuxa/$n.fullchain.pem 2>/dev/null
done
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
OUT="$(nginx -t 2>&1)" && ok "nginx -t accepts it" || { bad "nginx -t"; echo "$OUT" | sed 's/^/    /'; }
systemctl reload nginx
# A reload returns before nginx's old workers have finished; until they
# have, port 80 can still be answered from the configuration before it.
sleep 3

echo
echo "==> the suite, through nginx"
via() { curl -sk --resolve "$1:443:127.0.0.1" --resolve "$1:80:127.0.0.1" "${@:2}"; }
CODE=$(via mail.$D -o /dev/null -w '%{http_code}' https://mail.$D/.well-known/jmap)
case "$CODE" in 30*|200|401) ok "the mail server answers on https://mail.$D ($CODE)" ;; *) bad "https://mail.$D/.well-known/jmap answered $CODE" ;; esac
via admin.$D https://admin.$D/ | grep -q "content=\"https://mail.$D\"" && ok "the console, pointed at https://mail.$D" || bad "the console is not served through nginx"
via webmail.$D https://webmail.$D/api/health | grep -q '"ok":true' && ok "the webmail is healthy through nginx" || bad "the webmail is not healthy through nginx"
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
echo "==> again: converges, and rewrites the file for what is here"
rm $DIR/proxy/nginx-inbuxa.conf
OUT="$(/tmp/inbuxa install --domain $D --proxy snippets --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && [ -f $DIR/proxy/nginx-inbuxa.conf ] && ok "a second run converges and writes it again" || { bad "second run exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
