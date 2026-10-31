#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# inbuxa Relay on a machine of its own: inbuxa relay install, run again,
# status, and uninstall with and without --purge. The relay joins with a
# token made here, for a server that never answers: the join itself is the
# relay's own, offline step, and what is checked is everything the installer
# puts around it -- the user, the directories, the unit, the ports.
#
# Until a server release carries relay binaries, pass one, taken out of an
# image built from inbuxa-server's Dockerfile.relay:
#   RELAY_BINARY=path e2e/vm/run.sh e2e/cases/relay-host.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

BIN=/opt/inbuxa-relay/bin/inbuxa-relay
STATE=/var/lib/inbuxa-relay
UNIT=inbuxa-relay.service
[ -f /tmp/inbuxa-relay-test ] && export INBUXA_RELAY_BINARY=/tmp/inbuxa-relay-test
listening() { ss -Hltn "sport = :$1" | grep -q .; }

# A join token as the console makes one: base64url JSON, with a CA pin and a
# secret that nothing will ever check.
b64() { basenc --base64url -w0 | tr -d =; }
TOKEN=$(printf '{"v":1,"relayId":"e2e-relay","caSpki":"%s","secret":"%s","expiresAt":%d}' \
  "$(head -c32 /dev/urandom | b64)" "$(head -c32 /dev/urandom | b64)" $(( $(date +%s) + 3600 )) | b64)

echo "==> without a token, it refuses"
OUT="$(/tmp/inbuxa relay install --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && echo "$OUT" | grep -q "hasn't joined a server yet" && ok "refused: not joined, no token" || bad "rc $rc: $OUT"
[ -e $BIN ] && bad "a binary was put in place anyway" || ok "nothing was installed"

echo
echo "==> without --yes, nothing happens"
OUT="$(echo "$TOKEN" | /tmp/inbuxa relay install --token - 2>&1)"; rc=$?
[ $rc -eq 1 ] && echo "$OUT" | grep -q "Nothing has happened yet" && ok "it printed the plan and stopped" || bad "rc $rc: $OUT"
echo "$OUT" | grep -q "join the server that made the token" && ok "the plan joins" || bad "the plan doesn't join"
echo "$OUT" | grep -q -- "--link-listen" && ok "it says to keep the link port to the server" || bad "no word on the link port"

echo
echo "==> installing, joining from standard input"
OUT="$(echo "$TOKEN" | /tmp/inbuxa relay install --token - --yes 2>&1)"; rc=$?
echo "$OUT" | tail -14 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "exit $rc"; journalctl -u $UNIT -n 20 --no-pager -o cat | sed 's/^/    | /'; }
echo "$OUT" | grep -q "$TOKEN" && bad "the token was printed" || ok "the token wasn't printed"

echo
echo "==> what is here"
systemctl is-active --quiet $UNIT && ok "$UNIT is running" || bad "$UNIT is not running"
systemctl is-enabled --quiet $UNIT && ok "and starts at boot" || bad "not enabled"
PID=$(systemctl show -p MainPID --value $UNIT)
[ "$(ps -o user= -p "$PID" 2>/dev/null)" = inbuxa-relay ] && ok "as the inbuxa-relay user" || bad "as '$(ps -o user= -p "$PID" 2>/dev/null)'"
CAP=$(awk '/^CapEff/ {print $2}' /proc/$PID/status)
[ "$CAP" = 0000000000000400 ] && ok "with CAP_NET_BIND_SERVICE alone" || bad "CapEff $CAP"
[ "$(stat -c '%U %a' $BIN)" = "inbuxa-relay 755" ] && ok "the binary is the relay's own, 755" || bad "the binary is $(stat -c '%U %a' $BIN)"
[ "$(stat -c '%U %a' /opt/inbuxa-relay)" = "root 755" ] && ok "/opt/inbuxa-relay is root's" || bad "/opt/inbuxa-relay is $(stat -c '%U %a' /opt/inbuxa-relay)"
[ "$(stat -c '%U %a' $STATE)" = "inbuxa-relay 700" ] && ok "$STATE is the relay's alone" || bad "$STATE is $(stat -c '%U %a' $STATE)"
[ -f $STATE/identity.json ] && ok "it joined (identity.json)" || bad "no identity.json"
listening 443 && ok "the link listens on 443" || bad "nothing on 443"
listening 25 && bad "port 25 is open before the server sent its settings" || ok "port 25 stays closed until the server sends its settings"
getent passwd inbuxa-relay | grep -q nologin && ok "the user can't log in" || bad "$(getent passwd inbuxa-relay)"

echo
echo "==> status"
OUT="$(/tmp/inbuxa relay status 2>&1)"; rc=$?
echo "$OUT" | sed 's/^/    /'
[ $rc -eq 0 ] && ok "status (exit 0)" || bad "exit $rc"
echo "$OUT" | grep -q "Relay: *e2e-relay" && ok "the relay's own status, as its user" || bad "no relay status"
echo "$OUT" | grep -q "Joined: *waiting for the downstream" && ok "waiting for its server" || bad "not waiting"

echo
echo "==> run again, with no token and no binary: a converge"
PID=$(systemctl show -p MainPID --value $UNIT)
OUT="$(env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "converged (exit 0)" || bad "exit $rc: $OUT"
echo "$OUT" | grep -q "keep inbuxa-relay" && ok "it kept the binary" || bad "it didn't keep the binary: $OUT"
[ "$(systemctl show -p MainPID --value $UNIT)" = "$PID" ] && ok "and left the relay running as it was" || bad "it restarted the relay"

echo
echo "==> the link on another address, then run again without it"
OUT="$(env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --link-listen 127.0.0.1:8443 --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "moved (exit 0)" || bad "exit $rc: $OUT"
listening 8443 && ! listening 443 && ok "the link listens on 8443 only" || bad "$(ss -Hltn)"
OUT="$(env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --yes 2>&1)"; rc=$?
grep -q -- "--link-listen 127.0.0.1:8443" /etc/systemd/system/$UNIT && listening 8443 && ok "a run without --link-listen keeps it" || bad "it went back: $OUT"
env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --link-listen '[::]:443' --yes >/dev/null 2>&1
listening 443 && ok "and back to 443" || bad "not back on 443"

echo
echo "==> something else on port 25 is refused"
systemctl stop $UNIT
python3 -c 'import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("", 25)); s.listen(); time.sleep(30)' &
HOLDER=$!; sleep 1
OUT="$(env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --yes 2>&1)"; rc=$?
[ $rc -ne 0 ] && echo "$OUT" | grep -q "port 25 is held by python3" && ok "refused: port 25 is held by python3" || bad "rc $rc: $OUT"
kill $HOLDER; wait $HOLDER 2>/dev/null
env -u INBUXA_RELAY_BINARY /tmp/inbuxa relay install --yes >/dev/null 2>&1 && systemctl is-active --quiet $UNIT && ok "and once it's gone, it starts" || bad "it didn't start again"

echo
echo "==> uninstall keeps its state"
OUT="$(/tmp/inbuxa relay uninstall --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "uninstalled (exit 0)" || bad "exit $rc: $OUT"
[ ! -e /etc/systemd/system/$UNIT ] && [ ! -e /opt/inbuxa-relay ] && ok "the unit and the binary are gone" || bad "left behind: $(ls /etc/systemd/system/$UNIT /opt/inbuxa-relay 2>&1)"
[ -f $STATE/identity.json ] && getent passwd inbuxa-relay >/dev/null && ok "its state and its user are kept" || bad "its state or user went"
listening 443 && bad "something still listens on 443" || ok "nothing listens on 443"

echo
echo "==> installed again, it is the same relay: no token needed"
OUT="$(/tmp/inbuxa relay install --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ! echo "$OUT" | grep -q "join the server" && ok "back, without joining again" || bad "rc $rc: $OUT"
/tmp/inbuxa relay status 2>&1 | grep -q "Relay: *e2e-relay" && ok "as e2e-relay" || bad "a different relay"

echo
echo "==> uninstall --purge"
OUT="$(/tmp/inbuxa relay uninstall --purge 2>&1)"; rc=$?
[ $rc -eq 1 ] && echo "$OUT" | grep -q "Remove it in the console too" && ok "without --yes it says what it would do" || bad "rc $rc: $OUT"
OUT="$(/tmp/inbuxa relay uninstall --purge --yes 2>&1)"; rc=$?
echo "$OUT" | tail -8 | sed 's/^/    /'
[ $rc -eq 0 ] && ok "purged (exit 0)" || bad "exit $rc"
[ ! -e $STATE ] && ok "$STATE is gone" || bad "$STATE is still here"
getent passwd inbuxa-relay >/dev/null && bad "the user is still here" || ok "the user is gone"
OUT="$(/tmp/inbuxa relay status 2>&1)"; rc=$?
[ $rc -eq 1 ] && echo "$OUT" | grep -q "isn't installed here" && ok "status: not installed" || bad "rc $rc: $OUT"

echo
echo "relay-host: $pass passed, $fail failed"
[ $fail -eq 0 ]
