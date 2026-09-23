#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs
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

echo "==> copying it in"
vm_scp "$LAB/inbuxa" lab@127.0.0.1:/tmp/inbuxa >/dev/null
vm_scp "$ROOT/$CASE" lab@127.0.0.1:/tmp/case.sh >/dev/null
vm_ssh 'chmod +x /tmp/inbuxa /tmp/case.sh'

echo "==> running $(basename "$CASE")"
vm_ssh 'sudo -E bash /tmp/case.sh'
