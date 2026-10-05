# inbuxa-installer

> [!NOTE]
> Development happens on [git.coffeylabs.org/inbuxa/inbuxa-installer](https://git.coffeylabs.org/inbuxa/inbuxa-installer); the copy on GitHub is a read-only mirror.
> Report issues at **[git.coffeylabs.org/inbuxa/inbuxa-installer/issues](https://git.coffeylabs.org/inbuxa/inbuxa-installer/issues)**, and join discussions at **[community.coffeylabs.org](https://community.coffeylabs.org)**.

One program that installs the **inbuxa** suite on the machine you run it on:
the mail server, the administration console and the webmail, each as a
container or on the host, in any mixture.

    inbuxa survey                  what this machine is, as the installer sees it
    inbuxa install --dry-run …     what would happen, before any of it does
    inbuxa install …               do it

It only ever installs here. A second machine runs it too, and `inbuxa join`
points that machine at a server already running elsewhere.

Linux only, and it says so on any other system rather than reporting a
machine that does not exist. Debian, Red Hat and Arch families, and the
derivatives people run: Ubuntu, Fedora, Rocky, CentOS, CachyOS. It uses
whichever container runtime the distribution ships -- docker where there is
one, podman on the Red Hat family -- and refuses a shape the machine cannot
deliver, with the reason.

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

- **`install --yes`** -- carries the plan out, for container shapes: writes
  the deployment, fetches the images, brings the mail server up in bootstrap
  mode with a credential that exists only for that step, completes bootstrap,
  brings the rest up without it, exempts the front ends from the auto-ban,
  creates the first mailbox, writes `credentials.txt` and `dns.zone`, and
  then checks that all three answer.

- **`plan -f` / `apply -f` / `export`** -- a whole installation described in
  one file, however many machines. Each machine acts on its own part and
  prints the command to run on the others; it never reaches them. `plan`
  diffs the file against what is actually installed here and changes
  nothing; `apply` converges to it, adding and removing components;
  `export` writes the file from what is already here.
- **`status`** -- what is installed, what is running, and where those two
  disagree: a container stopped by hand, or a deployment nothing recorded
  installing. It exits non-zero when they disagree, so a machine can be
  asked in a script whether it still matches itself.

Not built yet: host installs, the terminal interface, `join`, `status`,
`upgrade`, `uninstall`. The design is in the inbuxa specification (§6.1 and
the installer draft); the phases are there too.

    inbuxa install --local --domain example.test --install-deps --yes

is the shortest thing that works today: the whole suite on loopback, with no
DNS and no certificates, on a machine that starts with nothing. Without
`--local` it takes the real ports, puts Caddy in front and obtains
certificates -- which `e2e/cases/install-public.sh` proves against a private
CA, with no internet and no public name involved.

## Building and testing

    go build ./cmd/inbuxa

The installer writes units, creates users and takes ports 25 and 443, so it
is tested on a throwaway virtual machine rather than on anybody's desk:

    e2e/vm/up.sh                              a Debian 13 machine, in qemu, as you
    DISTRO=fedora e2e/vm/up.sh                or fedora, rocky9, ubuntu2404, arch, debian12
    e2e/vm/run.sh e2e/cases/survey.sh         what it says about a machine
    e2e/vm/run.sh e2e/cases/deps.sh           the offer, and taking it
    e2e/vm/run.sh e2e/cases/install-local.sh   a whole suite, and signing in to it
    e2e/vm/run.sh e2e/cases/install-public.sh  the same with real ports and certificates
    e2e/vm/run.sh e2e/cases/topology.sh       growing and shrinking from a file
    e2e/vm/run.sh e2e/cases/status-export.sh  intent against reality, and the file
    OLD_REF=<commit> e2e/vm/run.sh e2e/cases/upgrade-volumes.sh
                                              an install from before #5, upgraded
    e2e/vm/down.sh                            remove it

Each case starts from a copy of the machine taken when it was new, so a run
is free to break it and a failure is the installer's rather than the last
run's leftovers. `e2e/vm/up.sh` needs qemu, KVM and xorriso; nothing needs
root on your machine.

## License

AGPL-3.0-or-later. Some of this began as [ihasmail-oneshot], which is ours
and under the same license.

[ihasmail-oneshot]: https://git.coffeylabs.org/inbuxa/ihasmail-oneshot
