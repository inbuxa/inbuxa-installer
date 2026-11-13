#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# The SELinux tier: drive the paths SELinux has stopped the installer on
# before, with SELinux enforcing, and prove it provoked no denial.
#
# The path that bit, and the one this leans on, is the server backup: the
# installer archives the server's two volumes by mounting them, read-only,
# into a throwaway container running tar (internal/apply/upgrade.go). On
# Fedora and Rocky that is refused unless the volumes carry a label the
# container may read -- which is exactly the kind of thing a container-naive
# change breaks without noticing on Debian. So: install in containers, take a
# backup, converge, and after each step read the audit log for AVC denials in
# the installer's footprint, and confirm SELinux is still enforcing.
#
# Only meaningful where SELinux enforces, so on Debian/Ubuntu/Arch it skips and
# passes. Run it on the family it is for:
#   DISTRO=fedora  e2e/vm/run.sh e2e/cases/selinux.sh
#   DISTRO=rocky9  e2e/vm/run.sh e2e/cases/selinux.sh
set -uo pipefail
export PATH="/usr/sbin:/sbin:$PATH"   # getenforce and ausearch live here

pass=0; fail=0
ok()  { echo "  ok   $*"; pass=$((pass+1)); }
bad() { echo "  FAIL $*"; fail=$((fail+1)); }

DIR=/var/lib/inbuxa

if ! command -v getenforce >/dev/null 2>&1 || [ "$(getenforce)" != Enforcing ]; then
  echo "SKIPPED: SELinux is not enforcing here ($(. /etc/os-release && echo "$PRETTY_NAME"))."
  echo "This tier runs on Fedora or Rocky, e.g. DISTRO=fedora e2e/vm/run.sh e2e/cases/selinux.sh"
  exit 0
fi
echo "==> SELinux is enforcing ($(. /etc/os-release && echo "$ID $VERSION_ID"))"
ok "getenforce reports Enforcing"
command -v ausearch >/dev/null 2>&1 \
  && ok "ausearch is present to read the audit log" \
  || bad "ausearch is missing (install the audit package); the denial scan cannot run"

# scan LABEL SINCE: fail if the installer provoked an AVC denial in its own
# footprint (its containers, or /var/lib/inbuxa) after the checkpoint.
scan() {
  local d
  d="$(ausearch -i -m avc,user_avc -ts "$2" 2>/dev/null \
        | grep -iE 'inbuxa|container_t|container_file_t|svirt|conmon' || true)"
  if [ -z "$d" ]; then
    ok "$1: no SELinux denials"
  else
    bad "$1: SELinux denied something"
    echo "$d" | sed 's/^/    /'
  fi
}
enforcing() { [ "$(getenforce)" = Enforcing ]; }

# Whichever runtime the installer chose (podman on this family).
rt() { command -v docker >/dev/null 2>&1 && echo docker || echo podman; }
compose() {
  if [ "$(rt)" = docker ]; then
    docker compose -f "$DIR/compose.yaml" "$@"
  else
    DOCKER_HOST=unix:///run/podman/podman.sock \
      /usr/local/lib/docker/cli-plugins/docker-compose -f "$DIR/compose.yaml" "$@"
  fi
}

echo
echo "==> installing the suite in containers"
C=$(date +%T)
OUT="$(/tmp/inbuxa install --local --domain example.test --install-deps --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "installed (exit 0)" || { bad "install exit $rc"; echo "$OUT" | tail -20 | sed 's/^/    /'; }
scan "install" "$C"
enforcing && ok "still enforcing after install" || bad "no longer enforcing after install"

STATE="$(compose ps --format '{{.Service}} {{.State}}' 2>/dev/null)"
for s in server console webmail; do
  grep -q "^$s running" <<<"$STATE" && ok "$s is running" || { bad "$s is not running"; echo "$STATE" | sed 's/^/    /'; }
done

echo
echo "==> taking a backup (archives the server's volumes through a container)"
C=$(date +%T)
OUT="$(/tmp/inbuxa backup --yes 2>&1)"; rc=$?
[ $rc -eq 0 ] && ok "backup (exit 0)" || { bad "backup exit $rc"; echo "$OUT" | tail -20 | sed 's/^/    /'; }
scan "backup (archiveVolumes)" "$C"
ls "$DIR"/backups/server-*.tar.gz >/dev/null 2>&1 && ok "an archive was written: $(ls "$DIR"/backups)" || bad "no archive in $DIR/backups"
# Whole listing first: grep -q stops at the first match, and under pipefail the
# cut-off tar would fail the check on its own.
LIST="$(tar -tzf "$DIR"/backups/server-*.tar.gz 2>/dev/null)"
grep -q "config.json" <<<"$LIST" && ok "with the server's configuration in it" || bad "the archive has no config.json"
# The backup stops the server and must start it again.
grep -q "^server running" <<<"$(compose ps --format '{{.Service}} {{.State}}' 2>/dev/null)" \
  && ok "the server is running again after the backup" || bad "the server did not come back after the backup"

echo
echo "==> running install again converges, under SELinux, without a denial"
C=$(date +%T)
/tmp/inbuxa install --local --domain example.test --yes >/dev/null 2>&1 && ok "converged (exit 0)" || bad "the second install failed"
scan "converge" "$C"

echo
echo "==> SELinux is still enforcing at the end"
enforcing && ok "getenforce still reports Enforcing" || bad "SELinux is no longer enforcing"

echo
echo "==> $pass passed, $fail failed"
[ "$fail" -eq 0 ]
