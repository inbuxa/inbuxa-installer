# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Shared by the four scripts beside it: how the lab machine is started,
# stopped and talked to, and which distribution it runs. Sourced, never run.
#
# One lab per distribution, each with its own directory, disk and ssh port,
# so Fedora and Debian can be up at once and a case can be run against both
# without either noticing the other.
#
# One machine at a time, identified by its pid file. qemu is given a monitor
# on a unix socket so a stop is a clean powerdown rather than pulling the
# plug on a filesystem we are about to copy.
#
# Keep the default VGA device. `-vga none` looks right for a headless machine
# and is not: this image's GRUB hands over to a kernel that never prints, and
# the machine resets in a loop at "Booting Debian GNU/Linux" forever. -display
# none is what makes it headless; the adapter has to be there.

# The distributions this installer claims to support: Debian and Red Hat and
# Arch, and the derivatives people actually run. Each is the distribution's
# own cloud image, which is cloud-init seeded and needs no interaction.
#
# Fedora and Rocky are here because they are where the interesting
# differences live: podman instead of docker, firewalld on by default, and --
# on Rocky -- a glibc older than the server binary needs.
distro_image() {
  case "${1:-debian13}" in
    debian13) echo "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2" ;;
    debian12) echo "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2" ;;
    ubuntu2404) echo "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img" ;;
    # Fedora's image name carries a build number that changes with every
    # release, so this is pinned rather than guessed; check the mirror index
    # when moving it.
    fedora) echo "https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2" ;;
    rocky9) echo "https://download.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud.latest.x86_64.qcow2" ;;
    arch) echo "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2" ;;
    *) echo "" ;;
  esac
}

# Each lab gets its own ssh port, so several can be up at once.
distro_port() {
  case "${1:-debian13}" in
    debian13) echo 2222 ;;
    debian12) echo 2223 ;;
    ubuntu2404) echo 2224 ;;
    fedora) echo 2225 ;;
    rocky9) echo 2226 ;;
    arch) echo 2227 ;;
    *) echo 2222 ;;
  esac
}

vm_running() {
  [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null
}

vm_start() {
  qemu-system-x86_64 \
    -enable-kvm -cpu host -m "$MEM" -smp "$VCPUS" \
    -drive "file=$DISK,if=virtio,format=qcow2" \
    -drive "file=$SEED,if=virtio,format=raw,readonly=on" \
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
    -device virtio-net-pci,netdev=n0 \
    -display none -serial "file:$LOG" \
    -monitor "unix:$LAB/monitor.sock,server,nowait" \
    -pidfile "$PIDFILE" \
    -daemonize
}

vm_stop() {
  vm_running || return 0
  printf 'system_powerdown\n' | timeout 5 socat - "unix-connect:$LAB/monitor.sock" >/dev/null 2>&1 \
    || printf 'system_powerdown\n' | timeout 5 nc -U "$LAB/monitor.sock" >/dev/null 2>&1 \
    || true
  for _ in $(seq 1 30); do
    vm_running || return 0
    sleep 1
  done
  # It had its chance.
  kill "$(cat "$PIDFILE")" 2>/dev/null || true
  sleep 1
}

vm_ssh() {
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=3 -o BatchMode=yes -o LogLevel=ERROR \
      -p "$SSH_PORT" lab@127.0.0.1 "$@"
}

vm_scp() {
  scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o LogLevel=ERROR -P "$SSH_PORT" "$@"
}
