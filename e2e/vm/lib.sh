# SPDX-FileCopyrightText: 2026 Coffey Labs
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Shared by the four scripts beside it: how the lab machine is started,
# stopped and talked to. Sourced, never run.
#
# One machine at a time, identified by its pid file. qemu is given a monitor
# on a unix socket so a stop is a clean powerdown rather than pulling the
# plug on a filesystem we are about to copy.
#
# Keep the default VGA device. `-vga none` looks right for a headless machine
# and is not: this image's GRUB hands over to a kernel that never prints, and
# the machine resets in a loop at "Booting Debian GNU/Linux" forever. -display
# none is what makes it headless; the adapter has to be there.

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
