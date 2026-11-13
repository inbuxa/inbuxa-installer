#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Build the installer, copy it into the lab machine, and run a case there.
#
#   e2e/vm/run.sh e2e/cases/containers.sh     rewind, then run that case
#   KEEP=1 e2e/vm/run.sh e2e/cases/hosts.sh   run it on the machine as it is
#
# The rewind is the point: every case starts on an identical machine, so a
# failure is the installer's and not the last run's leftovers.
set -euo pipefail
CASE="${1:?usage: run.sh e2e/cases/<case>.sh}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
. "$(dirname "$0")/lib.sh"

DISTRO="${DISTRO:-debian13}"
LAB="${LAB:-$HOME/.cache/inbuxa-lab/$DISTRO}"
MEM="${MEM:-4096}"; VCPUS="${VCPUS:-2}"; SSH_PORT="${SSH_PORT:-$(distro_port "$DISTRO")}"
DISK="$LAB/lab.qcow2"; CLEAN="$LAB/lab-clean.qcow2"; SEED="$LAB/seed.iso"
LOG="$LAB/console.log"; PIDFILE="$LAB/qemu.pid"

[ -f "$ROOT/$CASE" ] || { echo "no such case: $CASE" >&2; exit 1; }
[ "${KEEP:-}" = 1 ] || "$(dirname "$0")/reset.sh"
vm_running || { echo "the lab is not up -- run e2e/vm/up.sh" >&2; exit 1; }

echo "==> building the installer"
(cd "$ROOT" && GOOS=linux GOARCH=amd64 go build -o "$LAB/inbuxa" ./cmd/inbuxa)

# An upgrade case needs the installer as it was, too: OLD_REF builds that
# commit as /tmp/inbuxa-old.
if [ -n "${OLD_REF:-}" ]; then
  echo "==> building the installer at $OLD_REF"
  OLD_SRC="$(mktemp -d)"
  (cd "$ROOT" && git archive "$OLD_REF" | tar -x -C "$OLD_SRC")
  (cd "$OLD_SRC" && GOOS=linux GOARCH=amd64 go build -o "$LAB/inbuxa-old" ./cmd/inbuxa)
  rm -rf "$OLD_SRC"
fi

echo "==> copying it in"
vm_scp "$LAB/inbuxa" lab@127.0.0.1:/tmp/inbuxa >/dev/null
# A server build to install instead of the release (INBUXA_SERVER_BINARY in
# the case), for testing a server change before it is released.
if [ -n "${SERVER_BINARY:-}" ]; then
  vm_scp "$SERVER_BINARY" lab@127.0.0.1:/tmp/inbuxa-server-test >/dev/null
  vm_ssh 'chmod +x /tmp/inbuxa-server-test'
fi
# The console's and the webmail's release tarballs, the same way, for host
# installs of them before a release carries them (INBUXA_CONSOLE_TARBALL and
# INBUXA_WEBMAIL_TARBALL in the case).
[ -z "${CONSOLE_TARBALL:-}" ] || vm_scp "$CONSOLE_TARBALL" lab@127.0.0.1:/tmp/inbuxa-console-test.tar.gz >/dev/null
[ -z "${WEBMAIL_TARBALL:-}" ] || vm_scp "$WEBMAIL_TARBALL" lab@127.0.0.1:/tmp/inbuxa-webmail-test.tar.gz >/dev/null
# A relay binary (INBUXA_RELAY_BINARY in the case), until a release carries
# one: docker cp it out of an image built from Dockerfile.relay.
if [ -n "${RELAY_BINARY:-}" ]; then
  vm_scp "$RELAY_BINARY" lab@127.0.0.1:/tmp/inbuxa-relay-test >/dev/null
  vm_ssh 'chmod +x /tmp/inbuxa-relay-test'
fi
[ -z "${OLD_REF:-}" ] || vm_scp "$LAB/inbuxa-old" lab@127.0.0.1:/tmp/inbuxa-old >/dev/null
vm_scp "$ROOT/$CASE" lab@127.0.0.1:/tmp/case.sh >/dev/null
vm_ssh 'chmod +x /tmp/inbuxa /tmp/case.sh; [ ! -f /tmp/inbuxa-old ] || chmod +x /tmp/inbuxa-old'

echo "==> running $(basename "$CASE")"
# On Fedora/Rocky the case runs under SELinux enforcing; note the time first so
# the scan afterwards sees only what the case provoked, not the boot.
CKPT=""
if vm_selinux_enforcing; then CKPT="$(vm_audit_checkpoint)"; fi
rc=0
vm_ssh 'sudo -E bash /tmp/case.sh' || rc=$?

# The SELinux tier: a case that passed its own checks still fails the run if
# the installer made SELinux deny something -- a denial it happened to survive
# today is a latent break. SELINUX_IGNORE=1 downgrades that to a warning.
if [ -n "$CKPT" ]; then
  echo "==> SELinux: denials since the case began"
  DEN="$(vm_selinux_denials "$CKPT")"
  if [ -n "$DEN" ]; then
    echo "$DEN" | sed 's/^/    /'
    if [ "${SELINUX_IGNORE:-}" = 1 ]; then
      echo "    SELINUX_IGNORE=1: not failing the run on these."
    else
      echo "    the installer must not provoke SELinux denials (SELINUX_IGNORE=1 to override)" >&2
      rc=1
    fi
  else
    echo "    none -- SELinux stayed out of the installer's way"
  fi
fi
exit "$rc"
