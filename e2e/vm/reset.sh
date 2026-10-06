#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Rewind the lab machine to the copy up.sh kept. A few seconds, and the
# machine is as fresh as a new VPS -- which is what makes a test run free to
# create users, write units and take ports.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DISTRO="${DISTRO:-debian13}"
LAB="${LAB:-$HOME/.cache/inbuxa-lab/$DISTRO}"
MEM="${MEM:-4096}"; VCPUS="${VCPUS:-2}"; SSH_PORT="${SSH_PORT:-$(distro_port "$DISTRO")}"
DISK="$LAB/lab.qcow2"; CLEAN="$LAB/lab-clean.qcow2"; SEED="$LAB/seed.iso"
LOG="$LAB/console.log"; PIDFILE="$LAB/qemu.pid"

[ -f "$CLEAN" ] || { echo "no clean copy -- run e2e/vm/up.sh first" >&2; exit 1; }

vm_stop
cp --reflink=auto "$CLEAN" "$DISK"
: > "$LOG"
vm_start
for _ in $(seq 1 90); do vm_ssh true 2>/dev/null && break; sleep 2; done
vm_ssh true 2>/dev/null || { echo "no ssh after the rewind; see $LOG" >&2; exit 1; }
echo "==> clean: ssh lab@127.0.0.1 -p $SSH_PORT"
