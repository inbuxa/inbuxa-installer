# inbuxa-installer

<p align="center">
  <a href="LICENSE"><img alt="License: AGPL-3.0-or-later" src="https://img.shields.io/badge/license-AGPL--3.0--or--later-2dd4bf?style=flat-square"></a>
  <a href="https://git.coffeylabs.org/inbuxa/inbuxa-installer/releases/latest"><img alt="Latest release" src="https://img.shields.io/gitea/v/release/inbuxa/inbuxa-installer?gitea_url=https%3A%2F%2Fgit.coffeylabs.org&label=release&color=2dd4bf&style=flat-square"></a>
  <a href="https://docs.inbuxa.org/install/"><img alt="Documentation: docs.inbuxa.org" src="https://img.shields.io/badge/docs-docs.inbuxa.org-0ea5e9?style=flat-square"></a>
  <a href="https://community.coffeylabs.org/c/inbuxa/5"><img alt="Forum: community.coffeylabs.org" src="https://img.shields.io/badge/forum-community.coffeylabs.org-0f766e?style=flat-square"></a>
  <a href="https://discord.gg/nqcY4TKfAn"><img alt="Chat on Discord" src="https://img.shields.io/discord/1523538164084637797?label=discord&logo=discord&logoColor=white&color=5865f2&style=flat-square"></a>
</p>

> [!NOTE]
> Development happens on [git.coffeylabs.org/inbuxa/inbuxa-installer](https://git.coffeylabs.org/inbuxa/inbuxa-installer); the copy on GitHub is a read-only mirror.
> Report issues at **[git.coffeylabs.org/inbuxa/inbuxa-installer/issues](https://git.coffeylabs.org/inbuxa/inbuxa-installer/issues)**, join discussions at **[community.coffeylabs.org](https://community.coffeylabs.org)**, or chat on **[Discord](https://discord.gg/nqcY4TKfAn)**.

One program that installs the **inbuxa** suite on the machine you run it on:
the mail server, the administration console and the webmail, each as a
container or on the host, in any mixture.

    inbuxa                         the interface: all of the below, a screen at a time
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

- **`inbuxa`** on its own (or `inbuxa install` with no options) -- the
  interface: six screens, always in the same order. What it found on this
  machine; the names; which components go here and how, with what the
  machine can't do greyed out and the reason beside it; what answers on 80
  and 443; the whole plan, with the command that does the same without the
  interface; and the install, ticking off its steps, ending on the
  credentials and addresses, which stay in the terminal afterwards. Nothing
  happens until you answer y. It fills in the same options the flags do --
  it is not a second installer -- and it is built on the standard library
  alone, like the rest. Without a terminal it says which flags to use.

- **`--proxy snippets`** -- for a machine that already runs nginx or Caddy:
  nothing here takes 80 or 443, and the installer writes
  `<dir>/proxy/nginx-inbuxa.conf` and `<dir>/proxy/Caddyfile.fragment` for
  that web server to load, trusts it in front of the suite (its
  X-Forwarded-For, and an exemption from the auto-ban), and leaves loading
  and reloading to you. Once it is loaded, the mail server's own
  certificates are turned on in the console. `--proxy none` writes nothing.

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

- **`install --yes`** -- carries the plan out: writes the deployment,
  fetches the images, brings the mail server up in bootstrap mode with a
  credential that exists only for that step, completes bootstrap, brings the
  rest up without it, exempts the front ends from the auto-ban, creates the
  first mailbox, writes `credentials.txt` and `dns.zone`, and then checks
  that all three answer.
- **`install --server host`** -- the same, with the mail server as the
  release binary under systemd instead of a container, checked against a
  checksum in the source. It gets a user of its own, its configuration and
  data in `<dir>/server`, and a unit, `inbuxa-server.service`. During setup
  it is reachable from the machine itself and nowhere else; afterwards its
  plain HTTP is on loopback for the proxy and the webmail, 443 is left to
  the proxy, and the unit will not start without a configuration file.
- **`--console host`** -- the console's files from its release, checked
  against a pinned checksum, unpacked into `<dir>/console` and pointed at the
  server; the managed Caddy or your own web server (`--proxy snippets`)
  serves them. Nothing runs, so there is nothing to start.
- **`--webmail host`** -- the webmail from its release, checked the same way,
  in `<dir>/webmail`, owned by root and only read by the webmail, which runs
  as its own user under `inbuxa-webmail.service` on the installer's own
  Node 26 (in `/opt/inbuxa/node`), on 127.0.0.1:8080. With all three on the
  host and `--proxy snippets`, no container runtime is installed at all.

- **`join URL --key KEY`** -- the console and the webmail on this machine,
  for a mail server already running at URL on another one. It reaches that
  server only over its own HTTPS, with an API key a server administrator
  made in the console (`--key -` reads it from standard input, and
  `INBUXA_JOIN_KEY` keeps it out of the shell's history; it is never written
  down). Through it the front ends' addresses go into the server's
  `inbuxa:FrontEnds`, which registers their sign-in and lets the console
  call the server from the browser, with no restart and nothing changed on
  that machine by hand. It asks first, dry runs included, and refuses with
  the reason when the key is wrong, the server is too old, or it already
  lists other front ends (`--replace` to put these in their place). Each
  front end goes in a container or on the host (`--console host`, `--webmail
  host`), as with `install`. Needs a server release 2026.10.12.1 or newer.
  The interface does the same: skip the server on the Components screen and
  give its address and the key.

- **`plan -f` / `apply -f` / `export`** -- a whole installation described in
  one file, however many machines. Each machine acts on its own part and
  prints the command to run on the others; it never reaches them. `plan`
  diffs the file against what is actually installed here and changes
  nothing; `apply` converges to it, adding and removing components;
  `export` writes the file from what is already here.
- **`upgrade [server|console|webmail]`** -- moves what is installed here to
  the releases this installer installs; a newer installer brings newer
  releases. `--dry-run` shows each component's move first (so does
  `status`). The server goes first, then the console, then the webmail,
  each checked once it is running again and put back as it was if not,
  which stops the upgrade there: a container by its old image tag, the
  server on the host by its old binary (kept as `inbuxa-server.<version>`),
  the console and the webmail on the host by their old directory. Before
  the server, which may convert its store on first start, it is stopped and
  its own data archived into `<dir>/backups` (`--no-backup` to skip; a store
  kept elsewhere, PostgreSQL or S3, is backed up where it lives).

- **`uninstall [--purge]`** -- takes away what the installer put on this
  machine: containers, units, binaries (and the old ones kept for rollback),
  the console's and the webmail's files, the webmail's user, its Node. The
  data stays -- the server's volumes or directory, Caddy's certificates, the
  credentials and secrets -- with the install record marked removed, so
  `install` run again with the same options brings it all back. `--purge`
  removes the data too, all but `<dir>/backups`, which it names for you to
  delete. Docker, and the configuration your own web server loads, are left
  alone. To take one component off, run `install` again with it skipped.

- **`certs [--key KEY]`** -- the mail server's own certificate, the one its
  mail ports present: who issued it, until when, for which names, whether
  it verifies, and where the mail host resolves. Reading it needs nothing.
  Fixing it needs an API key made in the console, as `join` does (`--key -`
  or `INBUXA_API_KEY`; never written down): a new ACME order, which the
  server doesn't start again by itself once one has failed -- typically
  because DNS wasn't in place during the install -- or, on a domain not yet
  on ACME, ACME turned on where the managed Caddy or your own web server
  passes the challenges. It waits for the mail ports to present the new
  certificate. The console's and the webmail's certificates are the
  proxy's, which renews them itself.

- **`status`** -- what is installed, what is running, and where those two
  disagree: a container stopped by hand, or a deployment nothing recorded
  installing. It exits non-zero when they disagree, so a machine can be
  asked in a script whether it still matches itself.

Not built yet: moving the server between a container and the host. The design is in the inbuxa specification (§6.1 and
the installer draft); the phases are there too.

    inbuxa install --local --domain example.test --install-deps --yes

is the shortest thing that works today: the whole suite on loopback, with no
DNS and no certificates, on a machine that starts with nothing. Without
`--local` it takes the real ports, puts Caddy in front and obtains
certificates -- which `e2e/cases/install-public.sh` proves against a private
CA, with no internet and no public name involved.

## Download

Each release has static binaries for linux/amd64 and linux/arm64, and a
SHA256SUMS to check them against:

    V=v2026.10.14     # the newest: https://git.coffeylabs.org/inbuxa/inbuxa-installer/releases/latest
    curl -fLO https://git.coffeylabs.org/inbuxa/inbuxa-installer/releases/download/$V/inbuxa-linux-amd64
    curl -fLO https://git.coffeylabs.org/inbuxa/inbuxa-installer/releases/download/$V/SHA256SUMS
    sha256sum --check --ignore-missing SHA256SUMS
    install -m 755 inbuxa-linux-amd64 /usr/local/bin/inbuxa

A release is a `v<year>.<month>.<day>` tag on main; `.gitea/workflows/release.yml`
builds it with `scripts/build-release.sh`, which builds the same thing
locally.

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
    e2e/vm/run.sh e2e/cases/install-host.sh    the server on the host, the rest beside it
    e2e/vm/run.sh e2e/cases/join.sh           front ends joined to a server elsewhere
    e2e/vm/run.sh e2e/cases/join-host.sh      the same on the host, behind nginx, no containers
    e2e/vm/run.sh e2e/cases/interface.sh      the interface, driven key by key in a terminal
    e2e/vm/run.sh e2e/cases/proxy-snippets.sh  behind nginx, from the configuration it writes
    e2e/vm/run.sh e2e/cases/webmail-host.sh    the webmail on the host, and signing in to it
    e2e/vm/run.sh e2e/cases/all-host.sh        everything on the host, behind nginx, no containers
    OLD_REF=v2026.10.12.1 e2e/vm/run.sh e2e/cases/upgrade-containers.sh
                                              an older install moved to these releases
    OLD_REF=v2026.10.12.1 e2e/vm/run.sh e2e/cases/upgrade-host-server.sh
                                              the same on the host, and a bad release put back
    CONSOLE_TARBALL=… WEBMAIL_TARBALL=… e2e/vm/run.sh e2e/cases/upgrade-frontends-host.sh
                                              the console and the webmail on the host, moved on
    OLD_REF=v2026.10.12.3 e2e/vm/run.sh e2e/cases/rerun-host-server.sh
                                              install run again: restarted when it must be, only then
    e2e/vm/run.sh e2e/cases/uninstall-containers.sh  uninstalled, installed again on the data, purged
    e2e/vm/run.sh e2e/cases/uninstall-host.sh  the same with the server and the webmail on the host
    e2e/vm/run.sh e2e/cases/certs.sh          a failed order, reported, then fixed with a key
    e2e/vm/run.sh e2e/cases/topology.sh       growing and shrinking from a file
    e2e/vm/run.sh e2e/cases/status-export.sh  intent against reality, and the file
    OLD_REF=<commit> e2e/vm/run.sh e2e/cases/upgrade-volumes.sh
                                              an install from before #5, upgraded
    e2e/vm/down.sh                            remove it

`SERVER_BINARY=path/to/inbuxa e2e/vm/run.sh …` installs a server build
instead of the release wherever a case installs the server on the host (the
installer's `INBUXA_SERVER_BINARY`), for testing a server change before it
is released. `CONSOLE_TARBALL=` and `WEBMAIL_TARBALL=` do the same for the
console's and the webmail's release tarballs (`INBUXA_CONSOLE_TARBALL`,
`INBUXA_WEBMAIL_TARBALL`).

Each case starts from a copy of the machine taken when it was new, so a run
is free to break it and a failure is the installer's rather than the last
run's leftovers. `e2e/vm/up.sh` needs qemu, KVM and xorriso; nothing needs
root on your machine.

## License

AGPL-3.0-or-later. Some of this began as [ihasmail-oneshot], which is ours
and under the same license.

[ihasmail-oneshot]: https://git.coffeylabs.org/inbuxa/ihasmail-oneshot
