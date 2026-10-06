#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Stop the lab machine and remove its disks. The base image stays, so up.sh
# is quick the next time; --all takes that too.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

DISTRO="${DISTRO:-debian13}"
LAB="${LAB:-$HOME/.cache/inbuxa-lab/$DISTRO}"
SSH_PORT="${SSH_PORT:-$(distro_port "$DISTRO")}"
DISK="$LAB/lab.qcow2"; CLEAN="$LAB/lab-clean.qcow2"; SEED="$LAB/seed.iso"
LOG="$LAB/console.log"; PIDFILE="$LAB/qemu.pid"

vm_stop
rm -f "$DISK" "$CLEAN" "$SEED" "$PIDFILE" "$LAB/monitor.sock"
[ "${1:-}" = "--all" ] && rm -rf "$LAB"
echo "==> gone"
