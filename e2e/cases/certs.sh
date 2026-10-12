#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa certs: the install's order fails because the mail host points
# somewhere else, as it does when DNS isn't in place yet; certs reports that
# without a key, refuses a wrong one, fails while DNS is still wrong, and
# gets the certificate once it is fixed. The same lab as install-public.sh
# (Pebble for Let's Encrypt, a DNS stub), with the server on the host.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/certs.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DOMAIN=lab.test
MAIL=mx.lab.test          # not "mail": proves the names follow --mail-host
CONSOLE=console.lab.test
WEBMAIL=webmail.lab.test
DIR=/var/lib/inbuxa

# Whichever runtime this machine has. The installer picks docker where there
# is one and podman on the Red Hat family; a case that says "docker" only
# tests half the distributions it is run on.
#
# Asked at each call, not once at the top: these cases start on a machine
# with no runtime at all and install one along the way, so anything decided
# up here is decided before the answer exists.
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}
WORK=/tmp/lab
LABNET=inbuxa-e2e
LABSUBNET=172.31.254.0/24
HOSTIP=172.31.254.1       # this machine, as the lab network sees it

rm -rf "$WORK"; mkdir -p "$WORK"

echo "==> what the installer needs, before the lab"
/tmp/inbuxa deps --console container --webmail container --install >/dev/null 2>&1 || true
{ docker version >/dev/null 2>&1 || podman version >/dev/null 2>&1; } && ok "a container runtime is usable" || { bad "no docker"; exit 1; }

echo
echo "==> standing up a private CA and a DNS stub"
docker network create --subnet "$LABSUBNET" "$LABNET" >/dev/null 2>&1
# Pebble's own certificate names localhost and "pebble"; the server and Caddy
# reach it at this machine's address from another network, so it gets one for
# that address, signed by the test root that ships with it.
docker create --name inbuxa-e2e-extract ghcr.io/letsencrypt/pebble:latest >/dev/null 2>&1
docker cp inbuxa-e2e-extract:/test/certs "$WORK/pebble-certs" >/dev/null
docker cp inbuxa-e2e-extract:/test/config/pebble-config.json "$WORK/pebble-config.json" >/dev/null
docker rm inbuxa-e2e-extract >/dev/null
openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=pebble" \
  -keyout "$WORK/pebble-key.pem" -out "$WORK/pebble.csr" 2>/dev/null
openssl x509 -req -in "$WORK/pebble.csr" -days 2 -CA "$WORK/pebble-certs/pebble.minica.pem" \
  -CAkey "$WORK/pebble-certs/pebble.minica.key.pem" -CAcreateserial \
  -extfile <(printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$HOSTIP") \
  -out "$WORK/pebble-cert.pem" 2>/dev/null
python3 - "$WORK/pebble-config.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
c["pebble"].update(httpPort=80, tlsPort=443,
                   certificate="/work/pebble-cert.pem", privateKey="/work/pebble-key.pem")
json.dump(c, open(sys.argv[1], "w"))
PY
chmod -R a+r "$WORK"
docker run -d --name inbuxa-e2e-dns --network "$LABNET" --ip 172.31.254.3 \
  ghcr.io/letsencrypt/pebble-challtestsrv:latest \
  -defaultIPv4 "$HOSTIP" -defaultIPv6 "" -http01 "" -https01 "" -tlsalpn01 "" -doh "" >/dev/null
# Nonce rejection off: Pebble refuses 5% of nonces on purpose, and the server
# gives up an order on the first one rather than retrying.
docker run -d --name inbuxa-e2e-pebble --network "$LABNET" --ip 172.31.254.2 \
  -p 14000:14000 -p 15000:15000 -v "$WORK:/work:ro" \
  -e PEBBLE_VA_NOSLEEP=1 -e PEBBLE_WFE_NONCEREJECT=0 \
  ghcr.io/letsencrypt/pebble:latest -config /work/pebble-config.json -dnsserver 172.31.254.3:8053 >/dev/null
for _ in $(seq 1 30); do
  curl -sf --cacert "$WORK/pebble-certs/pebble.minica.pem" "https://$HOSTIP:14000/dir" >/dev/null && break
  sleep 1
done
curl -sf --cacert "$WORK/pebble-certs/pebble.minica.pem" "https://$HOSTIP:14000/dir" >/dev/null \
  && ok "Pebble answers at https://$HOSTIP:14000/dir" || { bad "Pebble did not come up"; exit 1; }

# The mail host points nowhere useful, so the server's HTTP-01 challenge
# can't reach it. The console's and the webmail's names point here, so
# Caddy's own certificates are not what fails.
BOGUS=172.31.254.99
curl -s -X POST -d "{\"host\":\"$MAIL\",\"addresses\":[\"$BOGUS\"]}" http://172.31.254.3:8055/add-a >/dev/null \
  && ok "the DNS stub sends $MAIL to $BOGUS" || bad "the DNS stub's management API"
# The CA resolves names through the stub; this machine, as an operator's
# would, through its own resolver, which is told the same.
echo "$BOGUS $MAIL" >> /etc/hosts

CA="$WORK/pebble-certs/pebble.minica.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/roots/0" > "$WORK/root.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/intermediates/0" > "$WORK/int.pem"
cat "$WORK/int.pem" "$WORK/root.pem" > "$WORK/chain.pem"
# The machine trusts the CA, as a real one trusts Let's Encrypt.
cp "$WORK/root.pem" /usr/local/share/ca-certificates/inbuxa-e2e-pebble.crt && update-ca-certificates >/dev/null 2>&1

echo
echo "==> installing, with the mail host pointing elsewhere"
OUT="$(/tmp/inbuxa install --domain "$DOMAIN" --mail-host "$MAIL" --console-host "$CONSOLE" \
  --webmail-host "$WEBMAIL" --server host --acme-directory "https://$HOSTIP:14000/dir" \
  --acme-ca-root "$CA" --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -6 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"
grep -q "no certificate yet for $MAIL" <<<"$OUT" && ok "it said there was no certificate yet" || bad "it did not say so"
grep -q "\"mailHost\": \"$MAIL\"" /etc/inbuxa/install.json && ok "the install recorded the mail host" || bad "no mailHost in install.json"

echo
echo "==> certs, with no key"
OUT="$(/tmp/inbuxa certs 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 1 ] && ok "exit 1: there is a problem" || bad "exit $rc"
grep -q "self-signed" <<<"$OUT" && ok "it names the self-signed certificate" || bad "no mention of self-signed"
grep -q "$MAIL resolves to $BOGUS" <<<"$OUT" && ok "it shows where $MAIL points" || bad "no resolution shown"
grep -q "needs an API key" <<<"$OUT" && ok "it says a key is needed" || bad "no word about a key"
OUT="$(/tmp/inbuxa certs --dry-run 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "would start a new certificate order" <<<"$OUT" && ok "--dry-run says what it would do" || bad "dry run exit $rc"

echo
echo "==> an API key, as an administrator makes one in the console"
SERVER=http://127.0.0.1:8081
wait_server() { for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' $SERVER/.well-known/jmap)" != 000 ] && return 0; sleep 2; done; return 1; }
AUSER=$(awk '/^administrator/ {print $2}' "$DIR/credentials.txt")
APASS=$(awk '/^administrator/{getline; print $2}' "$DIR/credentials.txt")
echo "INBUXA_HTTP_BASIC_AUTH=all" >> "$DIR/server.env"
systemctl restart inbuxa-server; wait_server
EXPIRES=$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
ANSWER=$(curl -s -u "$AUSER:$APASS" -H 'Content-Type: application/json' \
  -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"urn:inbuxa:jmap:registry\"],\"methodCalls\":[[\"x:ApiKey/set\",{\"create\":{\"k\":{\"description\":\"certs test\",\"expiresAt\":\"$EXPIRES\",\"permissions\":{\"@type\":\"Inherit\"}}}},\"0\"]]}" $SERVER/jmap/)
KEY=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' <<<"$ANSWER")
sed -i '/^INBUXA_HTTP_BASIC_AUTH=/d' "$DIR/server.env"
systemctl restart inbuxa-server; wait_server
[ -n "$KEY" ] && ok "an API key for the administrator" || { bad "no API key"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }

OUT="$(INBUXA_API_KEY=wrong-key /tmp/inbuxa certs 2>&1)"; rc=$?
[ $rc -eq 1 ] && grep -q "refused the API key" <<<"$OUT" && ok "a wrong key is refused, and says so" || { bad "wrong key: exit $rc"; echo "$OUT" | tail -4 | sed 's/^/    /'; }

echo
echo "==> with a key, while DNS is still wrong"
OUT="$(echo "$KEY" | /tmp/inbuxa certs --key - 2>&1)"; rc=$?
echo "$OUT" | tail -5 | sed 's/^/    /'
[ $rc -eq 1 ] && grep -q "no certificate for $MAIL after three minutes" <<<"$OUT" && ok "it gives up, and says why" || bad "exit $rc"
grep -q "journalctl -u inbuxa-server" <<<"$OUT" && ok "and where the server's log is" || bad "no log hint"

echo
echo "==> DNS fixed, with a key"
curl -s -X POST -d "{\"host\":\"$MAIL\"}" http://172.31.254.3:8055/clear-a >/dev/null
sed -i "s/^$BOGUS $MAIL\$/$HOSTIP $MAIL/" /etc/hosts
OUT="$(echo "$KEY" | /tmp/inbuxa certs --key - 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "exit 0" || bad "exit $rc"
grep -q "starting a new certificate order" <<<"$OUT" && ok "it started a new order" || bad "no new order"
grep -q "Done: port 993 now presents a certificate for $MAIL" <<<"$OUT" && ok "it reports the new certificate" || bad "no Done line"
out=$(echo | timeout 20 openssl s_client -connect "127.0.0.1:993" -servername "$MAIL" -verify_hostname "$MAIL" -CAfile "$WORK/chain.pem" 2>&1)
grep -q "Verify return code: 0 (ok)" <<<"$out" && ok "993 verifies for $MAIL" || bad "993 does not verify"
grep -rqF "$KEY" "$DIR" /etc/inbuxa 2>/dev/null && bad "the API key was written down" || ok "the API key was not kept"

echo
echo "==> certs again, nothing to do"
OUT="$(/tmp/inbuxa certs 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "Nothing to do" <<<"$OUT" && ok "nothing to do, without a key" || { bad "exit $rc"; echo "$OUT" | sed 's/^/    /'; }

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
