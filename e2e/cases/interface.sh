#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The interface, driven the way a person drives it: `inbuxa` with no
# arguments in a terminal (a pseudo-terminal here), keys pressed one screen at
# a time, on a machine with nothing on it. Loopback evaluation, everything
# else as it comes.
#
# What only the interface can get wrong is checked: each screen arrives, the
# machine's missing container runtime is offered rather than refused, the
# plan shows the command that does the same, nothing happens before the "y",
# the install finishes on screen, the terminal is given back, and the
# summary is left in it. Then what install-local.sh checks of the result.
#
# Run from the host with: e2e/vm/run.sh e2e/cases/interface.sh
set -uo pipefail

pass=0; fail=0
ok()   { echo "  ok   $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa
command -v python3 >/dev/null || { bad "no python3 on this machine to drive a terminal with"; echo "==> $pass passed, $fail failed"; exit 1; }

cat > /tmp/drive.py <<'PY'
import os, pty, re, select, struct, sys, time, fcntl, termios

pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.execv("/tmp/inbuxa", ["/tmp/inbuxa"])
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 32, 110, 0, 0))

seen = ""
def plain(s):
    return re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", s)
def pump(t):
    global seen
    end = time.time() + t
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.1)
        if r:
            try:
                seen += os.read(fd, 65536).decode("utf-8", "replace")
            except OSError:
                return False
    return True
def frame():
    return plain(seen.split("\x1b[H")[-1])
def wait_for(text, timeout, label):
    # Words, not lines: the interface wraps to the window, and a phrase can
    # be split across two lines of it.
    end = time.time() + timeout
    while time.time() < end:
        if text in " ".join(frame().replace("|", " ").split()):
            print("OK", label); return True
        if not pump(0.5):
            break
    print("FAIL", label, "-- never saw:", text)
    print("\n".join("    | " + l for l in frame().split("\r\n")[:30]))
    return False
def keys(s):
    os.write(fd, s.encode()); pump(0.4)

ENTER, DOWN = "\r", "\x1b[B"
ok = wait_for("What this machine allows", 30, "screen 1: what it found")
keys(ENTER)
ok &= wait_for("Where it will be reached", 10, "screen 2: names")
keys(DOWN * 6 + " ")
ok &= wait_for("example.test", 5, "loopback fills an evaluation domain")
keys(ENTER)
ok &= wait_for("What goes on this machine", 10, "screen 3: components")
ok &= wait_for("will install what is missing", 5, "the missing runtime is offered, not refused")
keys(ENTER)
ok &= wait_for("publishes nothing", 10, "screen 4: reach, on loopback")
keys(ENTER)
ok &= wait_for("Nothing has happened yet", 10, "screen 5: the plan")
keys("\x1b[6~" * 4)
ok &= wait_for("--local --domain example.test --install-deps --yes", 5, "the plan shows the command that does the same")
keys(ENTER)
ok &= wait_for("Install now?", 5, "a confirmation before anything happens")
print("OK nothing installed before the y" if not os.path.exists("/var/lib/inbuxa/compose.yaml") else "FAIL something was written before the y")
keys("y")
ok &= wait_for("Done.", 1500, "screen 6: the install finishes on screen")
keys("q")
pump(2)
after = plain(seen.split("\x1b[?1049l")[-1]) if "\x1b[?1049l" in seen else ""
print("OK the terminal is given back" if "\x1b[?1049l" in seen and "\x1b[?25h" in seen else "FAIL the terminal was not restored")
print("OK the summary is left in the terminal" if "administrator" in after and "credentials.txt" in after else "FAIL no summary after the interface closed: " + repr(after[-300:]))
_, status = os.waitpid(pid, 0)
code = os.waitstatus_to_exitcode(status)
print(("OK" if code == 0 else "FAIL") + f" exit status {code}")
PY

echo "==> the interface, from a machine with nothing on it"
OUT="$(python3 /tmp/drive.py 2>&1)"
while IFS= read -r line; do
  case "$line" in
    OK\ *)   ok "${line#OK }" ;;
    FAIL\ *) bad "${line#FAIL }" ;;
    *)       echo "$line" ;;
  esac
done <<<"$OUT"

echo
echo "==> what it installed"
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
STATE="$($(rt) compose -f $DIR/compose.yaml ps --format '{{.Service}} {{.State}}' 2>/dev/null)"
for s in server console webmail; do
  grep -q "^$s running" <<<"$STATE" && ok "$s is running" || { bad "$s is not running"; echo "$STATE" | sed 's/^/    /'; }
done
[ "$(stat -c %a $DIR/credentials.txt 2>/dev/null)" = 600 ] && ok "credentials.txt, 600" || bad "credentials.txt missing or not 600"
grep -q '"shapes"' /etc/inbuxa/install.json 2>/dev/null && ok "the install is recorded" || bad "no install record"
/tmp/inbuxa status >/dev/null 2>&1 && ok "status: intent and reality agree" || bad "status disagrees"
curl -fsS http://127.0.0.1:8080/api/health 2>/dev/null | grep -q '"ok":true' && ok "the webmail is healthy" || bad "the webmail is not healthy"

echo
echo "==> without a terminal it says which flags do the same"
OUT="$(/tmp/inbuxa </dev/null 2>&1)"; rc=$?
[ $rc -eq 2 ] && grep -q "install --help" <<<"$OUT" && ok "no terminal: a sentence, exit 2" || bad "no terminal: exit $rc, $OUT"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
