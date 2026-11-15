#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# #65: a client that connects over IPv6 has to reach the server with its own
# address. On an IPv4-only project network Docker relays IPv6 connections
# through docker-proxy, and the server saw every one of them as the network's
# gateway, 172.31.253.1: SPF and DMARC were checked against that, IPv6
# senders shared one set of bans and rate limits, and with --proxy snippets
# the gateway is an address the server is told never to ban.
#
# A second network namespace stands in for the internet, with an IPv6 and an
# IPv4 address of its own, and opens SMTP sessions to this machine. The
# server's log says which address each one came from.
#
# With OLD_REF (any installer from before the fix) the bug is shown first and
# the fixed installer then converges that install:
#   OLD_REF=v2026.10.15.2 e2e/vm/run.sh e2e/cases/ipv6-sources.sh
# Without it, a fresh install is checked.
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
D=example.test
CLIENT6=fd00:99::2; HOST6=fd00:99::1
CLIENT4=10.99.0.2;  HOST4=10.99.0.1
compose() { docker compose -f "$DIR/compose.yaml" "$@"; }

ip netns add outside
ip link add v-host type veth peer name v-out
ip link set v-out netns outside
ip addr add $HOST6/64 dev v-host nodad; ip addr add $HOST4/24 dev v-host; ip link set v-host up
ip -n outside addr add $CLIENT6/64 dev v-out nodad; ip -n outside addr add $CLIENT4/24 dev v-out
ip -n outside link set v-out up; ip -n outside link set lo up

# One SMTP session from outside to $1, then the address the server logged
# for it: the client's own, or the gateway's.
seen_as() {
  local to=$1 before
  before=$(compose logs server 2>&1 | wc -l)
  ip netns exec outside python3 - "$to" <<'PY' >/dev/null 2>&1
import smtplib, sys
s = smtplib.SMTP(sys.argv[1], 25, timeout=20); s.ehlo("outside.test"); s.quit()
PY
  sleep 2
  compose logs server 2>&1 | tail -n +$((before + 1)) \
    | grep -oE "$CLIENT6|$CLIENT4|172\.31\.253\.1\b|fd69:6e62:7578::1\b" | head -1
}

check_sources() {
  local got
  got=$(seen_as $HOST6)
  [ "$got" = "$CLIENT6" ] && ok "an IPv6 client reaches the server as itself ($got)" \
    || bad "an IPv6 client reaches the server as '${got:-nothing in the log}', not $CLIENT6"
  got=$(seen_as $HOST4)
  [ "$got" = "$CLIENT4" ] && ok "an IPv4 client still reaches it as itself ($got)" \
    || bad "an IPv4 client reaches the server as '${got:-nothing in the log}', not $CLIENT4"
}

echo "==> a machine with nginx already on it"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx libnginx-mod-stream openssl >/dev/null 2>&1
systemctl is-active --quiet nginx && ok "nginx is running" || { bad "no nginx"; echo "==> $pass passed, $fail failed"; exit 1; }

if [ -x /tmp/inbuxa-old ]; then
  echo
  echo "==> installed by the installer from before the fix"
  OUT="$(/tmp/inbuxa-old install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
  [ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -20 | sed 's/^/    /'; }
  got=$(seen_as $HOST6)
  [ "$got" = "172.31.253.1" ] && ok "the bug, shown: an IPv6 client reached the server as the gateway" \
    || bad "the old install did not show the bug: an IPv6 client was seen as '${got:-nothing}'"
fi

echo
echo "==> installed (or converged) by this installer"
OUT="$(/tmp/inbuxa install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
echo "$OUT" | grep -v '^    |' | grep -iE "ipv6|docker|network|exit|error" | head -12 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -25 | sed 's/^/    /'; }
grep -q "enable_ipv6: true" $DIR/compose.yaml && grep -q "subnet: fd69:6e62:7578::/64" $DIR/compose.yaml \
  && ok "compose.yaml gives the project network IPv6" || bad "no IPv6 in compose.yaml"
[ "$(docker network inspect inbuxa_stack --format '{{.EnableIPv6}}' 2>/dev/null)" = true ] \
  && ok "the network Docker made has IPv6" || bad "the network inbuxa_stack has no IPv6"
v=$(docker version --format '{{.Server.Version}}')
if [ "${v%%.*}" -lt 27 ]; then
  grep -q '"ip6tables": true' /etc/docker/daemon.json && grep -q '"experimental": true' /etc/docker/daemon.json \
    && ok "Docker $v: ip6tables and experimental turned on in daemon.json" || bad "Docker $v: daemon.json not changed: $(cat /etc/docker/daemon.json 2>&1)"
fi
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:8081/healthz/live >/dev/null && break; sleep 2; done
check_sources

echo
echo "==> a second run changes nothing"
OUT="$(/tmp/inbuxa install --domain $D --proxy snippets --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "second run (exit 0)" || { bad "exit $rc"; echo "$OUT" | tail -15 | sed 's/^/    /'; }
grep -q "already forwards IPv6" <<<"$OUT" && bad "the IPv6 step ran again on Docker that forwards" || ok "no IPv6 step the second time"
check_sources

ip netns del outside
echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
