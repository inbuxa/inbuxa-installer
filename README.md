# inbuxa-installer

One program that installs the **inbuxa** suite on the machine you run it on:
the mail server, the administration console and the webmail, each as a
container or on the host, in any mixture.

    inbuxa survey                  what this machine is, as the installer sees it
    inbuxa install --dry-run …     what would happen, before any of it does
    inbuxa install …               do it

It only ever installs here. A second machine runs it too, and `inbuxa join`
points that machine at a server already running elsewhere.

## What works today

This is early. What is built:

- **`survey`** -- the machine's facts: distribution, init, whether Docker is
  usable *by this user*, Node's version, which of the suite's ports are free
  and what holds the ones that are not, memory, disk, and whether an install
  is already recorded here. It changes nothing.
- **`install --dry-run`** -- the whole plan: every file, unit, container,
  port, DNS record and credential, and a refusal with a reason when the
  machine cannot carry out what was asked.
- **`deps`** -- what a shape needs that this machine has not got, and, with
  `--install`, the doing of it: the Docker daemon from the distribution's own
  archive, the Compose plugin and Node from their official builds, both
  pinned by version and checked against a checksum in the source before
  anything is put in place. `install --install-deps` does the same as part of
  a run. A missing dependency is an offer, not a refusal.

Not built yet: applying the plan, the terminal interface, `join`, `status`,
`upgrade`, `uninstall`. The design is in the inbuxa specification (§6.1 and
the installer draft); the phases are there too.

## Building and testing

    go build ./cmd/inbuxa

The installer writes units, creates users and takes ports 25 and 443, so it
is tested on a throwaway virtual machine rather than on anybody's desk:

    e2e/vm/up.sh                        a Debian 13 machine, in qemu, as you
    e2e/vm/run.sh e2e/cases/survey.sh   rewind it, then run a case inside
    e2e/vm/down.sh                      remove it

Each case starts from a copy of the machine taken when it was new, so a run
is free to break it and a failure is the installer's rather than the last
run's leftovers. `e2e/vm/up.sh` needs qemu, KVM and xorriso; nothing needs
root on your machine.

## License

AGPL-3.0-or-later. Some of this began as [ihasmail-oneshot], which is ours
and under the same license.

[ihasmail-oneshot]: https://git.coffeylabs.org/inbuxa/ihasmail-oneshot
