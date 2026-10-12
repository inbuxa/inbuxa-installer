#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Everything on the host, nothing in a container: the server and the webmail
# under systemd, the console as files, behind the nginx this machine already
# runs (--proxy snippets). No container runtime is installed at all.
#
# Until the console's and the webmail's releases carry their tarballs, pass
# them: CONSOLE_TARBALL=… WEBMAIL_TARBALL=… e2e/vm/run.sh e2e/cases/all-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test
[ -f /tmp/inbuxa-console-test.tar.gz ] && export INBUXA_CONSOLE_TARBALL=/tmp/inbuxa-console-test.tar.gz
[ -f /tmp/inbuxa-webmail-test.tar.gz ] && export INBUXA_WEBMAIL_TARBALL=/tmp/inbuxa-webmail-test.tar.gz

echo "==> a machine with nginx, and no container runtime"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx openssl >/dev/null 2>&1
systemctl is-active --quiet nginx && ok "nginx is running" || { bad "no nginx"; exit 1; }

echo
echo "==> installing everything on the host"
OUT="$(/tmp/inbuxa install --domain $D --server host --console host --webmail host --proxy snippets --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -16 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
command -v docker >/dev/null || command -v podman >/dev/null && bad "a container runtime was installed" || ok "no container runtime was installed"
[ -f $DIR/compose.yaml ] && bad "there is a compose project" || ok "no compose project"
for u in inbuxa-server inbuxa-webmail; do systemctl is-active --quiet $u && ok "$u is running" || bad "$u is not running"; done
grep -q "name=\"api-base-url\" content=\"https://mail.$D\"" $DIR/console/index.html && ok "the console's files name https://mail.$D" || bad "the console's index.html"

echo
echo "==> behind nginx, from the configuration it wrote"
mkdir -p /etc/ssl/inbuxa
for n in mail.$D admin.$D webmail.$D; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$n" \
    -keyout /etc/ssl/inbuxa/$n.key.pem -out /etc/ssl/inbuxa/$n.fullchain.pem 2>/dev/null
done
cp $DIR/proxy/nginx-inbuxa.conf /etc/nginx/conf.d/inbuxa.conf
nginx -t >/dev/null 2>&1 && ok "nginx -t accepts it" || { bad "nginx -t"; nginx -t 2>&1 | sed 's/^/    /'; }
systemctl reload nginx; sleep 3
via() { curl -sk --resolve "$1:443:127.0.0.1" "${@:2}"; }
PAGE=$(via admin.$D https://admin.$D/)
grep -q "content=\"https://mail.$D\"" <<<"$PAGE" && ok "the console is served from its files" || bad "the console is not served"
CODE=$(via admin.$D -o /dev/null -w '%{http_code}' https://admin.$D/some/route/in/the/app)
[ "$CODE" = 200 ] && ok "and its routes fall back to the app" || bad "an app route answered $CODE"
ASSET=$(grep -o '/assets/[^"]*\.js' <<<"$PAGE" | head -1)
via admin.$D -D - -o /dev/null "https://admin.$D$ASSET" | grep -qi "cache-control: public, immutable" && ok "hashed assets are cached for good" || bad "no immutable cache on $ASSET"
via webmail.$D https://webmail.$D/api/health | grep -q '"ok":true' && ok "the webmail is healthy through nginx" || bad "the webmail through nginx"
CODE=$(via mail.$D -o /dev/null -w '%{http_code}' https://mail.$D/.well-known/jmap)
case "$CODE" in 30*|200|401) ok "the mail server through nginx ($CODE)" ;; *) bad "the server through nginx: $CODE" ;; esac

echo
echo "==> status"
STATUS="$(/tmp/inbuxa status 2>&1)"; rc=$?
for k in server console webmail; do grep -q "$k.*installed as a host" <<<"$STATUS" && ok "status: the $k is a host install" || bad "status: $k"; done
[ $rc -eq 0 ] && ok "intent and reality agree" || { bad "status exit $rc"; echo "$STATUS" | sed 's/^/    /'; }

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
