#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Renaming an install to names on another domain (#81-#84): install runs again
# with only the new names and keeps the rest; dns says its saved records predate
# the rename; certs orders the new mail host's certificate on the other domain,
# adding it for certificates only; and once the server's hostname is changed the
# records follow. Pebble for Let's Encrypt and a DNS stub, as in certs.sh.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/rename.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DOMAIN=lab.test
MAIL=mx.lab.test
CONSOLE=console.lab.test
WEBMAIL=webmail.lab.test
DIR=/var/lib/inbuxa
NEWMAIL=mail.other.test   # on another domain from the install's
NEWCONSOLE=console.other.test
NEWWEBMAIL=webmail.other.test

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

# Every name resolves to this machine, for the CA (through the stub) and for
# this machine's own resolver.
for n in $MAIL $CONSOLE $WEBMAIL $NEWMAIL $NEWCONSOLE $NEWWEBMAIL; do echo "$HOSTIP $n" >> /etc/hosts; done

CA="$WORK/pebble-certs/pebble.minica.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/roots/0" > "$WORK/root.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/intermediates/0" > "$WORK/int.pem"
cat "$WORK/int.pem" "$WORK/root.pem" > "$WORK/chain.pem"
# The machine trusts the CA, as a real one trusts Let's Encrypt.
cp "$WORK/root.pem" /usr/local/share/ca-certificates/inbuxa-e2e-pebble.crt && update-ca-certificates >/dev/null 2>&1

echo
echo "==> installing on $DOMAIN"
OUT="$(/tmp/inbuxa install --domain "$DOMAIN" --mail-host "$MAIL" --console-host "$CONSOLE" \
  --webmail-host "$WEBMAIL" --server host --acme-directory "https://$HOSTIP:14000/dir" \
  --acme-ca-root "$CA" --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -6 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; exit 1; }
grep -q '"proxy": "caddy"' /etc/inbuxa/install.json && ok "the record has the proxy (#84)" || bad "no proxy in install.json"
grep -q "\"consoleHost\": \"$CONSOLE\"" /etc/inbuxa/install.json && ok "and the console's name" || bad "no consoleHost in install.json"

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

echo
echo "==> install again with only the new names (#84)"
PLAN="$(/tmp/inbuxa install --mail-host "$NEWMAIL" --console-host "$NEWCONSOLE" --webmail-host "$NEWWEBMAIL" --dry-run 2>&1)"; rc=$?
echo "$PLAN" | sed -n '1,14p' | sed 's/^/    /'
[ $rc -eq 0 ] && ok "the plan builds without --domain" || bad "dry run exit $rc"
grep -q "Already installed here" <<<"$PLAN" && ok "it says the rest comes from the install" || bad "no note on inherited options"
grep -Eq -- "--server +host" <<<"$PLAN" && ok "the server stays on the host" || bad "server shape not inherited"
grep -Eq -- "--acme-directory +https://$HOSTIP:14000/dir" <<<"$PLAN" && ok "and keeps the private CA" || bad "ACME directory not inherited"
grep -qi "moving the mail server" <<<"$PLAN" && bad "it planned a move" || ok "no move planned"
OUT="$(/tmp/inbuxa install --mail-host "$NEWMAIL" --console-host "$NEWCONSOLE" --webmail-host "$NEWWEBMAIL" --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -6 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "renamed (exit 0)" || bad "exit $rc"
grep -q '"server": "host"' /etc/inbuxa/install.json && ok "the server is still on the host" || bad "shape changed"
grep -q "\"mailHost\": \"$NEWMAIL\"" /etc/inbuxa/install.json && ok "the record has the new mail host" || bad "mailHost not updated"
grep -q "$NEWCONSOLE" "$DIR/Caddyfile" && grep -q "http://$NEWMAIL" "$DIR/Caddyfile" && ok "Caddy serves the new names" || bad "Caddyfile has old names"
for _ in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/chain.pem" "https://$NEWCONSOLE/")
  [ "$code" = 200 ] && break; sleep 2
done
[ "$code" = 200 ] && ok "the console answers at https://$NEWCONSOLE ($code)" || bad "console at new name: $code"

echo
echo "==> dns, with the records saved before the rename (#83)"
OUT="$(/tmp/inbuxa dns 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "don't name $NEWMAIL" <<<"$OUT" && ok "it says they predate the rename" || { bad "dns exit $rc"; echo "$OUT" | tail -4 | sed 's/^/    /'; }

echo
echo "==> certs for the new mail host, on another domain (#82)"
OUT="$(echo "$KEY" | /tmp/inbuxa certs --key - 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "exit 0" || bad "exit $rc"
grep -q "adding other.test to the server for certificates only" <<<"$OUT" && ok "it added other.test for certificates" || bad "no certificate-only domain"
grep -q "Done: port 993 now presents a certificate for $NEWMAIL" <<<"$OUT" && ok "it reports the new certificate" || bad "no Done line"
out=$(echo | timeout 20 openssl s_client -connect "127.0.0.1:993" -servername "$NEWMAIL" -verify_hostname "$NEWMAIL" -CAfile "$WORK/chain.pem" 2>&1)
grep -q "Verify return code: 0 (ok)" <<<"$out" && ok "993 verifies for $NEWMAIL" || bad "993 does not verify for $NEWMAIL"

echo
echo "==> the server's hostname changed, as in the console, and the records follow"
jmap() { curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"using\":[\"urn:ietf:params:jmap:core\",\"urn:inbuxa:jmap:registry\"],\"methodCalls\":[$1]}" $SERVER/jmap/; }
ANSWER=$(jmap "[\"x:SystemSettings/set\",{\"update\":{\"singleton\":{\"defaultHostname\":\"$NEWMAIL\"}}},\"0\"]")
grep -q '"updated"' <<<"$ANSWER" && ok "hostname set to $NEWMAIL" || { bad "hostname not set"; head -c 300 <<<"$ANSWER" | sed 's/^/    /'; }
OUT="$(echo "$KEY" | /tmp/inbuxa dns --refresh --key - 2>&1)"; rc=$?
[ $rc -eq 0 ] && grep -q "$NEWMAIL" <<<"$OUT" && ok "dns --refresh names $NEWMAIL" || { bad "refresh exit $rc"; echo "$OUT" | tail -6 | sed 's/^/    /'; }
grep -q "don't name" <<<"$OUT" && bad "it still says they don't name it" || ok "and doesn't call them stale"
OUT="$(/tmp/inbuxa dns 2>&1)"
grep -q "don't name" <<<"$OUT" && bad "the saved copy is still stale" || ok "the saved copy was rewritten"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
