#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Build the throwaway machine the installer is tested on.
#
# The installer writes units, creates users, takes 25 and 443 and can install
# a web server. None of that belongs on a workstation, and none of it can be
# proved in a container either -- systemd, users and ports are the thing under
# test. So: a distribution's own cloud image in qemu, seeded with cloud-init,
# with a copy of the disk kept the moment it is up. Every run starts from that
# copy, so a run can break the machine as thoroughly as it likes.
#
# DISTRO picks which: debian13 (the default), debian12, ubuntu2404, fedora,
# rocky9 or arch. They are not interchangeable, which is the point -- podman
# rather than docker on the Red Hat family, firewalld on by default there, and
# a glibc on Rocky 9 older than the server binary needs. Each has its own
# directory and ssh port, so several can be up at once.
#
#   DISTRO=fedora e2e/vm/up.sh
#   e2e/vm/up.sh       build it and keep a clean copy (idempotent)
#   e2e/vm/reset.sh    back to the clean copy, a few seconds
#   e2e/vm/run.sh      build the installer, copy it in, run a case inside
#   e2e/vm/down.sh     stop it and remove its disks
#
# Plain qemu, run as you, rather than libvirt: no root, no storage pool, no
# permissions to argue with, and the logs are readable. Networking is qemu's
# user-mode NAT with ssh forwarded to 127.0.0.1:2222, which gives the guest
# the internet it needs to pull images without putting it on the LAN.
#
# Needs: qemu-system-x86_64, /dev/kvm, xorriso, ssh, curl.
set -euo pipefail

. "$(dirname "$0")/lib.sh"

DISTRO="${DISTRO:-debian13}"
LAB="${LAB:-$HOME/.cache/inbuxa-lab/$DISTRO}"
MEM="${MEM:-4096}"
VCPUS="${VCPUS:-2}"
DISK_GB="${DISK_GB:-20}"
SSH_PORT="${SSH_PORT:-$(distro_port "$DISTRO")}"
BASE_URL="${BASE_URL:-$(distro_image "$DISTRO")}"
BASE="$LAB/base.qcow2"
DISK="$LAB/lab.qcow2"
CLEAN="$LAB/lab-clean.qcow2"
SEED="$LAB/seed.iso"
LOG="$LAB/console.log"
PIDFILE="$LAB/qemu.pid"
KEY="${KEY:-$HOME/.ssh/id_ed25519.pub}"

say() { echo "==> $*"; }

[ -n "$BASE_URL" ] || { echo "unknown distribution '$DISTRO' (debian13, debian12, ubuntu2404, fedora, rocky9, arch)" >&2; exit 1; }
[ -r "$KEY" ] || { echo "no public key at $KEY (set KEY=)" >&2; exit 1; }
[ -w /dev/kvm ] || { echo "no writable /dev/kvm -- is this user in the kvm group?" >&2; exit 1; }

if vm_running; then
  say "already up on 127.0.0.1:$SSH_PORT -- reset.sh rewinds it, down.sh removes it"
  exit 0
fi

mkdir -p "$LAB"
if [ ! -f "$BASE" ]; then
  say "fetching the base image (once)"
  curl -fL --progress-bar -o "$BASE.part" "$BASE_URL"
  mv "$BASE.part" "$BASE"
fi

# cloud-init: one unprivileged user with our key and passwordless sudo, and
# almost nothing else. The installer is what is under test, so the machine
# should be as plain as a new VPS -- if the installer needs a package, the
# installer should say so rather than the lab quietly providing it.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/meta-data" <<EOF
instance-id: inbuxa-lab
local-hostname: inbuxa-lab
EOF

cat > "$WORK/user-data" <<EOF
#cloud-config
users:
  - name: lab
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$KEY")
package_update: true
packages:
  - curl
  - ca-certificates
  - openssl
  - python3
growpart:
  mode: auto
  devices: ['/']
resize_rootfs: true
write_files:
  - path: /etc/inbuxa-lab
    content: |
      This machine exists to be broken by e2e/vm/run.sh. Nothing on it is kept.
EOF

say "building the seed"
xorriso -as mkisofs -quiet -o "$SEED" -V CIDATA -J -r "$WORK/user-data" "$WORK/meta-data"

# An overlay on the base, so the download is written once and every rebuild is
# instant. The base itself is never modified.
say "building the disk"
rm -f "$DISK"
qemu-img create -q -f qcow2 -F qcow2 -b "$BASE" "$DISK" "${DISK_GB}G"

say "starting the machine"
vm_start

say "waiting for ssh on 127.0.0.1:$SSH_PORT"
for _ in $(seq 1 90); do
  if vm_ssh true 2>/dev/null; then break; fi
  sleep 2
done
vm_ssh true 2>/dev/null || { echo "no ssh after three minutes; see $LOG" >&2; exit 1; }

say "waiting for cloud-init to finish"
vm_ssh 'sudo cloud-init status --wait >/dev/null 2>&1 || true'

say "keeping a clean copy of the disk"
vm_stop
cp --reflink=auto "$DISK" "$CLEAN"
vm_start
for _ in $(seq 1 90); do
  if vm_ssh true 2>/dev/null; then break; fi
  sleep 2
done

say "up: ssh lab@127.0.0.1 -p $SSH_PORT  (console log: $LOG)"
vm_ssh 'echo "    guest:" $(. /etc/os-release && echo $PRETTY_NAME) "| kernel" $(uname -r) "| disk" $(df -h / | awk "NR==2{print \$2}")'
