// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package relay puts inbuxa Relay on this machine: the edge relay that takes
// mail in for a server elsewhere and sends its mail on (the relay spec,
// docs/spec/features/relay.md in inbuxa-server, RL-31).
//
// A relay is a machine of its own. It has nothing of the suite's: no
// compose project, no proxy, no state file. What is here is the binary, its
// user, its state directory and its unit, and the console sets everything
// else once it has joined. So it is a command of its own, not a fourth
// component of install.
//
// The binary is the release's static musl build, checked against the
// checksum pinned in this installer and against the inbuxa release keys. It
// belongs to the relay's own user, as the server's unit expects, so that an
// upgrade sent from the console can replace it in place; an install run
// again leaves a newer one alone.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

const (
	Unit     = "inbuxa-relay.service"
	User     = "inbuxa-relay"
	Home     = "/opt/inbuxa-relay"
	BinDir   = Home + "/bin"
	Binary   = BinDir + "/inbuxa-relay"
	StateDir = "/var/lib/inbuxa-relay"

	DefaultLinkListen = "[::]:443"

	unitPath = "/etc/systemd/system/" + Unit
)

// Log is where the steps are reported, as apply.Log.
type Log interface {
	Step(format string, a ...any)
	Info(format string, a ...any)
	Out() io.Writer
}

// Options are what install is asked for.
type Options struct {
	// Token is the join token the console showed, for a relay that hasn't
	// joined yet. A joined relay doesn't need one.
	Token string
	// LinkListen is where the relay listens for the server's link. Empty
	// keeps what the unit already says, or [::]:443.
	LinkListen string
}

// Plan is what install would do here.
type Plan struct {
	Version    string // the release this installer installs, as the relay names it
	Installed  string // what the binary here says it is, or ""
	Fetch      bool   // put the release binary in place
	Local      string // INBUXA_RELAY_BINARY: a build to test instead
	LinkListen string
	Joined     bool
	Join       bool // join with the token
	Running    bool
	Steps      []string
	Notes      []string
}

func (p Plan) String() string {
	var b strings.Builder
	b.WriteString("inbuxa Relay on this machine:\n")
	for _, s := range p.Steps {
		b.WriteString("  - " + s + "\n")
	}
	if len(p.Notes) > 0 {
		b.WriteString("\nWorth knowing:\n")
		for _, s := range p.Notes {
			b.WriteString("  - " + s + "\n")
		}
	}
	return b.String()
}

// Check works out what install would do, and refuses what it can't do.
func Check(ctx context.Context, o Options) (Plan, error) {
	p := Plan{Version: release.RelayVersion(), Local: os.Getenv("INBUXA_RELAY_BINARY")}
	if os.Geteuid() != 0 {
		return p, errors.New("the relay needs root to install, and to look at what is here")
	}
	if !host.Systemd() {
		return p, errors.New("the relay runs as a systemd service, and systemd doesn't run this machine; " +
			"run its container image instead (docs.inbuxa.org: inbuxa Relay)")
	}
	arch := runtime.GOARCH
	sum, ok := release.RelaySHA256[arch]
	if !ok {
		return p, fmt.Errorf("there is no relay for %s: it is built for amd64 and arm64", arch)
	}
	if st, err := state.Load(); err == nil {
		if _, ok := st.Shapes["server"]; ok && !st.Removed() {
			return p, errors.New("the mail server is installed on this machine, and the relay would take its port 25: " +
				"a relay goes on a machine of its own, in front of the server")
		}
	}

	p.LinkListen = o.LinkListen
	if p.LinkListen == "" {
		p.LinkListen = unitLinkListen(readFile(unitPath))
	}
	if p.LinkListen == "" {
		p.LinkListen = DefaultLinkListen
	}
	if _, port, err := net.SplitHostPort(p.LinkListen); err != nil || port == "" {
		return p, fmt.Errorf("--link-listen %s: an address and port, like [::]:443 or [fd42:1::2]:443", p.LinkListen)
	}

	_, err := os.Stat(filepath.Join(StateDir, "identity.json"))
	p.Joined = err == nil
	p.Running = active(ctx)
	p.Installed = installedVersion(ctx, Binary)

	switch {
	case p.Local != "":
		p.Fetch = true
		p.Steps = append(p.Steps, fmt.Sprintf("put %s in %s instead of the release, as INBUXA_RELAY_BINARY says (no checksum, no signature)", p.Local, BinDir))
	case p.Installed == "":
		p.Fetch = true
		p.Steps = append(p.Steps, fmt.Sprintf("fetch inbuxa-relay %s for %s, check its checksum and its signature, and put it in %s", p.Version, arch, BinDir))
	default:
		c, ok := compareVersions(p.Installed, p.Version)
		switch {
		case !ok || c < 0:
			p.Fetch = true
			p.Steps = append(p.Steps, fmt.Sprintf("replace inbuxa-relay %s with %s, checked as a new install is", p.Installed, p.Version))
		case c == 0:
			p.Steps = append(p.Steps, fmt.Sprintf("keep inbuxa-relay %s, the release this installer installs", p.Installed))
		default:
			p.Steps = append(p.Steps, fmt.Sprintf("keep inbuxa-relay %s, which is newer than this installer's %s (an upgrade from the console)", p.Installed, p.Version))
		}
	}
	if p.Fetch && p.Local == "" && sum == "" {
		return p, fmt.Errorf("this installer's release, inbuxa-server %s, has no relay to install for %s; "+
			"an installer from a later release has", release.ServerVersion, arch)
	}
	if !userExists(ctx) {
		p.Steps = append(p.Steps, "user "+User+" (system, no login), which owns the binary so an upgrade from the console can replace it")
	}
	p.Steps = append(p.Steps,
		StateDir+" for its state, readable by that user alone",
		fmt.Sprintf("%s: ports 25 and %s and nothing more, the rest of the machine read-only to it", Unit, portOf(p.LinkListen)))

	switch {
	case !p.Joined && o.Token == "":
		return p, errors.New("this relay hasn't joined a server yet: add it in the console (Settings › Mail flow › Sending › Relays, " +
			"Add a relay…) and pass the join token it shows as --token -, which reads it from standard input")
	case !p.Joined:
		p.Join = true
		p.Steps = append(p.Steps, "join the server that made the token")
	case o.Token != "":
		p.Notes = append(p.Notes, "This relay has joined a server already, so the token isn't used. To join another, "+
			"`inbuxa relay uninstall --purge` first.")
	}
	switch {
	case !p.Running:
		p.Steps = append(p.Steps, "start it, and at every boot")
	case p.Fetch:
		p.Steps = append(p.Steps, "restart it on the new binary")
	}

	if !p.Running {
		for _, addr := range []string{":25", p.LinkListen} {
			if why := held(addr); why != "" {
				return p, fmt.Errorf("port %s is %s; the relay needs it", portOf(addr), why)
			}
		}
	}
	if p.LinkListen == DefaultLinkListen {
		p.Notes = append(p.Notes, "The link listens on port 443 of every address. Let only the server's nodes reach it, "+
			"with a firewall, or put it on WireGuard with --link-listen [ADDRESS]:443.")
	}
	return p, nil
}

// Install carries out a plan from Check.
func Install(ctx context.Context, p Plan, o Options, log Log) error {
	if !userExists(ctx) {
		if err := run(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", StateDir, "--shell", nologin(), User); err != nil {
			return fmt.Errorf("creating the %s user: %w", User, err)
		}
		log.Info("user %s (system, no login)", User)
	}
	for _, d := range []struct {
		path, owner string
		mode        os.FileMode
	}{{Home, "root", 0o755}, {BinDir, User, 0o755}, {StateDir, User, 0o700}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return err
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
		if err := run(ctx, "chown", d.owner+":"+d.owner, d.path); err != nil {
			return err
		}
	}

	if p.Fetch {
		if err := putBinary(ctx, p, log); err != nil {
			return err
		}
	}

	unitChanged, err := writeIfChanged(unitPath, unitFile(p.LinkListen), 0o644)
	if err != nil {
		return err
	}
	if unitChanged {
		log.Info("%s, link on %s", unitPath, p.LinkListen)
		if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}

	if p.Join {
		log.Step("joining")
		out, err := exec.CommandContext(ctx, "runuser", "-u", User, "--",
			Binary, "join", o.Token, "--state", StateDir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("the join was refused: %s", strings.TrimSpace(strings.TrimPrefix(string(out), "inbuxa-relay: ")))
		}
		log.Info("%s", strings.TrimSpace(string(out)))
	}

	if err := run(ctx, "systemctl", "enable", "--quiet", Unit); err != nil {
		return err
	}
	switch {
	case !p.Running:
		log.Step("starting it")
		err = run(ctx, "systemctl", "start", Unit)
	case p.Fetch || unitChanged:
		log.Step("restarting it")
		err = run(ctx, "systemctl", "restart", Unit)
	}
	if err != nil {
		return fmt.Errorf("%w\n%s", err, journal(ctx, 20))
	}
	// Restart=always would hide a relay that stops at once behind a unit
	// that is forever starting: give it the time to fail, then look for
	// the same process.
	pid := mainPID(ctx)
	time.Sleep(3 * time.Second)
	if !active(ctx) || pid == "0" || mainPID(ctx) != pid {
		return fmt.Errorf("the relay didn't stay up; its log:\n%s", journal(ctx, 20))
	}
	return nil
}

// putBinary puts the binary in place: the release's, checked, or a local
// build. It is written beside the old one and renamed over it, so a failure
// part way leaves the old one running.
func putBinary(ctx context.Context, p Plan, log Log) error {
	work, err := os.MkdirTemp("", "inbuxa-relay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	path := filepath.Join(work, "inbuxa-relay")
	if p.Local != "" {
		log.Step("installing %s instead of the release, as INBUXA_RELAY_BINARY says (no checksum, no signature)", p.Local)
		b, err := os.ReadFile(p.Local)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, b, 0o755); err != nil {
			return err
		}
	} else {
		arch := runtime.GOARCH
		log.Step("fetching inbuxa-relay %s for %s", p.Version, arch)
		url := release.RelayBinary(arch)
		if err := deps.FetchVerified(ctx, log.Out(), url, release.RelaySHA256[arch], path, 0o755); err != nil {
			return err
		}
		sig, err := fetchSmall(ctx, url+".sig")
		if err != nil {
			return fmt.Errorf("its signature: %w", err)
		}
		keys, err := ParseKeys(release.RelayKeys)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := Verify(b, sig, keys, p.Version, release.RelayTarget(arch)); err != nil {
			return fmt.Errorf("%s: %w -- nothing was installed", url, err)
		}
		fmt.Fprintln(log.Out(), "    signed by an inbuxa release key")
	}
	// Run before it is put in place, as the relay tries a binary before an
	// upgrade.
	got := installedVersion(ctx, path)
	if got == "" {
		out, _ := exec.CommandContext(ctx, path, "--version").CombinedOutput()
		return fmt.Errorf("the relay binary does not run here: %s", strings.TrimSpace(string(out)))
	}
	if p.Local == "" && got != p.Version {
		return fmt.Errorf("the relay binary says it is %s, not %s", got, p.Version)
	}
	next := filepath.Join(BinDir, ".inbuxa-relay.new")
	if err := run(ctx, "install", "-o", User, "-g", User, "-m", "0755", path, next); err != nil {
		return err
	}
	if err := os.Rename(next, Binary); err != nil {
		os.Remove(next)
		return err
	}
	log.Info("%s, inbuxa-relay %s", Binary, got)
	return nil
}

// Status prints the relay's own account of itself. It is false when there is
// no relay here.
func Status(ctx context.Context, out io.Writer) bool {
	v := installedVersion(ctx, Binary)
	if v == "" {
		fmt.Fprintln(out, "inbuxa Relay isn't installed here.")
		return false
	}
	state := "stopped"
	if active(ctx) {
		state = "running"
	}
	enabled, _ := exec.CommandContext(ctx, "systemctl", "is-enabled", Unit).Output()
	fmt.Fprintf(out, "%s: %s, %s at boot\n", Unit, state, strings.TrimSpace(string(enabled)))
	fmt.Fprintf(out, "%s: inbuxa-relay %s (this installer installs %s)\n\n", Binary, v, release.RelayVersion())
	cmd := exec.CommandContext(ctx, "runuser", "-u", User, "--", Binary, "status", "--state", StateDir)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Run()
	return true
}

// UninstallPlan is what uninstall would take away and keep.
func UninstallPlan(purge bool) (remove, keep []string) {
	remove = []string{Unit + ", stopped and disabled", Home + " (the binary, and the previous one an upgrade keeps)"}
	if purge {
		remove = append(remove, StateDir+": its link keys, its settings and the mail it holds", "the "+User+" user")
	} else {
		keep = []string{StateDir + ": it still belongs to its server, and holds any mail it was holding; --purge removes it"}
	}
	return remove, keep
}

// Installed says whether there is a relay here to take away.
func Installed() bool {
	for _, p := range []string{unitPath, Binary} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Uninstall takes the relay away. With purge it resets it first, which
// refuses while it holds mail unless discardHeld: that mail is sealed to its
// server, and nobody can read it after.
func Uninstall(ctx context.Context, purge, discardHeld bool, log Log) error {
	wasRunning := active(ctx)
	log.Step("stopping %s", Unit)
	if err := run(ctx, "systemctl", "disable", "--now", "--quiet", Unit); err != nil && wasRunning {
		return err
	}
	if purge {
		if _, err := os.Stat(Binary); err == nil {
			args := []string{"-u", User, "--", Binary, "reset", "--state", StateDir}
			if discardHeld {
				args = append(args, "--discard-held")
			}
			out, err := exec.CommandContext(ctx, "runuser", args...).CombinedOutput()
			if err != nil {
				msg := strings.TrimSpace(strings.TrimPrefix(string(out), "inbuxa-relay: "))
				msg = strings.ReplaceAll(msg, "Run `inbuxa-relay reset --discard-held`", "Add --discard-held")
				if wasRunning {
					run(ctx, "systemctl", "enable", "--now", "--quiet", Unit)
					msg += " The relay is running again, as it was."
				}
				return fmt.Errorf("nothing was removed: %s", msg)
			}
			log.Info("%s", strings.TrimSpace(string(out)))
		}
	}
	for _, p := range []string{unitPath, "/etc/systemd/system/" + Unit + ".d"} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	log.Info("%s removed", Unit)
	if err := os.RemoveAll(Home); err != nil {
		return err
	}
	log.Info("%s removed", Home)
	if !purge {
		return nil
	}
	if err := os.RemoveAll(StateDir); err != nil {
		return err
	}
	log.Info("%s removed", StateDir)
	if userExists(ctx) {
		if err := run(ctx, "userdel", User); err != nil {
			return fmt.Errorf("removing the %s user: %w", User, err)
		}
		log.Info("user %s removed", User)
	}
	return nil
}

// unitFile is the unit, the one inbuxa-server ships as
// resources/systemd/inbuxa-relay.service, with the link where it was asked.
func unitFile(linkListen string) string {
	execStart := Binary + " run --state " + StateDir
	if linkListen != DefaultLinkListen {
		execStart += " --link-listen " + linkListen
	}
	return `# Written by the inbuxa installer, as inbuxa-server ships it in
# resources/systemd/inbuxa-relay.service. Changes here are replaced on its
# next run: put yours in a drop-in, with systemctl edit inbuxa-relay.
#
# The binary lives in ` + BinDir + `, owned by the ` + User + ` user, so an
# upgrade from the console can replace it; the relay then exits, and
# Restart= starts the new one. It may bind ports 25 and 443 and nothing more.
[Unit]
Description=inbuxa Relay
Documentation=https://docs.inbuxa.org/
Conflicts=postfix.service sendmail.service exim4.service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + execStart + `
Restart=always
RestartSec=2
SyslogIdentifier=inbuxa-relay
User=` + User + `
Group=` + User + `
StateDirectory=inbuxa-relay
StateDirectoryMode=0700
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=` + BinDir + `
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`
}

var linkListenArg = regexp.MustCompile(`(?m)^ExecStart=\S+ run .*--link-listen (\S+)`)

// unitLinkListen is the link address a unit written before names, so a run
// without --link-listen keeps it.
func unitLinkListen(unit string) string {
	if m := linkListenArg.FindStringSubmatch(unit); m != nil {
		return m[1]
	}
	return ""
}

// installedVersion is the version a relay binary says it is, or "" when it
// isn't there or doesn't run.
func installedVersion(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return parseVersionOutput(string(out))
}

func parseVersionOutput(out string) string {
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != "inbuxa-relay" {
		return ""
	}
	return f[1]
}

// held says why an address can't be listened on, or "" when it can.
func held(addr string) string {
	l, err := net.Listen("tcp", addr)
	if err == nil {
		l.Close()
		return ""
	}
	if errors.Is(err, syscall.EADDRNOTAVAIL) {
		return "on an address this machine doesn't have"
	}
	var port int
	fmt.Sscan(portOf(addr), &port)
	if h := host.HolderOf(port); h != "" {
		return "held by " + h
	}
	return "in use"
}

func portOf(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return port
}

func active(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", Unit).Run() == nil
}

func mainPID(ctx context.Context) string {
	out, _ := exec.CommandContext(ctx, "systemctl", "show", "-p", "MainPID", "--value", Unit).Output()
	return strings.TrimSpace(string(out))
}

func journal(ctx context.Context, lines int) string {
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", Unit, "-n", fmt.Sprint(lines),
		"--no-pager", "-o", "cat").CombinedOutput()
	return string(out)
}

func userExists(ctx context.Context) bool {
	return exec.CommandContext(ctx, "getent", "passwd", User).Run() == nil
}

// fetchSmall is a small file, such as a .sig, read whole.
func fetchSmall(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), err
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func nologin() string {
	for _, p := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/usr/bin/nologin"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/bin/false"
}

func writeIfChanged(path, content string, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		return false, os.Chmod(path, mode)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return false, err
	}
	return true, os.Chmod(path, mode)
}

func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
