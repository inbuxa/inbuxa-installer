#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The public shape, with no internet involved: Pebble stands in for Let's
# Encrypt and a DNS stub answers every name with this machine's address, so
# both Caddy and the mail server really obtain certificates, over the real
# ports, through the real Caddyfile.
#
# --local proves the pieces talk to each other. This proves the part an
# operator actually gets wrong: ports 25 and 443 held for real, two programs
# asking the same CA for certificates for overlapping names, and a proxy in
# front of all of it. The two are kept apart by challenge type -- Caddy uses
# TLS-ALPN-01 on 443, the server HTTP-01 on 80, which Caddy forwards -- and
# this is where that is checked.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/install-public.sh
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

echo
echo "==> installing the public shape"
OUT="$(/tmp/inbuxa install --domain "$DOMAIN" --mail-host "$MAIL" --console-host "$CONSOLE" \
  --webmail-host "$WEBMAIL" --acme-directory "https://$HOSTIP:14000/dir" \
  --acme-ca-root "$WORK/pebble-certs/pebble.minica.pem" --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | tail -22 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || bad "exit $rc"

echo
echo "==> the ports an operator would expect"
for p in 25 80 443 465 993 995 4190; do
  ss -ltn "sport = :$p" | grep -q LISTEN && ok "$p is listening" || bad "$p is not listening"
done

# Caddy obtains its certificates in the background, so give the first
# handshake for each name a little room.
expect() {
  local want=$1 got=; shift
  for _ in $(seq 1 40); do
    got=$(curl -s -o /dev/null -w '%{http_code}' "$@" 2>/dev/null || true)
    [ "$got" = "$want" ] && return 0
    sleep 1
  done
  echo "       got HTTP ${got:-nothing}, wanted $want" >&2
  return 1
}

CA="$WORK/pebble-certs/pebble.minica.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/roots/0" > "$WORK/root.pem"
curl -sf --cacert "$CA" "https://$HOSTIP:15000/intermediates/0" > "$WORK/int.pem"
cat "$WORK/int.pem" "$WORK/root.pem" > "$WORK/chain.pem"

echo
echo "==> the front ends, over HTTPS, with certificates from the CA"
expect 200 --cacert "$WORK/chain.pem" --resolve "$WEBMAIL:443:127.0.0.1" "https://$WEBMAIL/api/health" \
  && ok "the webmail answers over HTTPS" || bad "the webmail does not answer over HTTPS"
expect 200 --cacert "$WORK/chain.pem" --resolve "$CONSOLE:443:127.0.0.1" "https://$CONSOLE/" \
  && ok "the console answers over HTTPS" || bad "the console does not answer over HTTPS"
expect 308 --cacert "$WORK/chain.pem" --resolve "$WEBMAIL:80:127.0.0.1" "http://$WEBMAIL/" \
  && ok "port 80 redirects" || bad "port 80 does not redirect"

echo
echo "==> the mail server's own certificate, on the mail ports"
grep -q "certificate    issued by" <<<"$OUT" && ok "the install reported a certificate" || bad "the install reported no certificate"
for port in 993 465; do
  out=$(echo | timeout 20 openssl s_client -connect "127.0.0.1:$port" -servername "$MAIL" \
    -verify_hostname "$MAIL" -CAfile "$WORK/chain.pem" 2>&1)
  grep -q "Verify return code: 0 (ok)" <<<"$out" \
    && ok "$port presents a certificate for $MAIL, issued by the CA" \
    || { bad "$port did not verify"; grep -E "Verify return code|subject=|issuer=" <<<"$out" | sed 's/^/    /'; }
done

echo
echo "==> sign-in goes to the mail server's own page, as a registered client"
# Unlike --local, this shape has OAuth: the webmail refuses a password of its
# own and sends the browser to the server, as the first-party client the
# server registered for this URL. A 302 to the mail host is that working.
USER=$(awk '/^first mailbox/ {print $3}' "$DIR/credentials.txt")
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' --cacert "$WORK/chain.pem" \
  --resolve "$WEBMAIL:443:127.0.0.1" --resolve "$MAIL:443:127.0.0.1" \
  "https://$WEBMAIL/api/auth/oauth/start?username=$USER&remember=1")
grep -q "^https://$MAIL/" <<<"$LOC" && ok "the webmail sends sign-in to $MAIL" || bad "sign-in went to '${LOC:-nowhere}'"
grep -q "client_id=ihasmail-inbuxa" <<<"$LOC" && ok "as the first-party client" || bad "no first-party client id in '$LOC'"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$WORK/chain.pem" --resolve "$MAIL:443:127.0.0.1" "$LOC")
[ "$CODE" = 200 ] && ok "and the server serves that page over its own certificate" || bad "the server answered $CODE for the sign-in page"

echo
echo "==> and nothing was left behind that should not be"
ENVOUT="$("$(rt)" inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$(compose ps -q server)")"
grep -q "RECOVERY_ADMIN" <<<"$ENVOUT" && bad "the server still carries a recovery admin" || ok "no recovery admin on the server"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
