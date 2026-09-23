// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package host looks at the machine the installer is running on and reports
// what it found. Nothing here changes anything.
//
// This is the first screen of the interface and the floor under every choice
// after it: a component can only offer "container" if there is a usable
// Docker, or "host" if this is a systemd machine with the pieces that shape
// needs. The survey is taken once, so the plan and the interface are arguing
// from the same facts.
package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Facts is everything the installer knows about this machine before it asks
// anybody anything.
type Facts struct {
	OS         OSInfo
	Root       bool    // running as uid 0
	Systemd    bool    // systemd is pid 1
	Runtime    Runtime // docker or podman, whichever this machine can use
	Node       Node    //
	Glibc      Glibc   // what a downloaded binary has to be able to run against
	Ports      []Port  // the ports the suite wants, and who holds them
	DiskFreeGB int     // on the filesystem that would hold the install
	MemoryMB   int
	Existing   string // path of a previous install's state file, or ""
}

// OSInfo is the distribution, as os-release describes it.
type OSInfo struct {
	ID        string // "debian", "ubuntu", "fedora"…
	VersionID string // "13", "24.04"
	Pretty    string
	Family    string // "debian", "rhel", "arch", "suse", "" when unknown
}

// Runtime is whether containers are a real option for this user, and with
// what. Docker and podman are both first-class: podman is what the Red Hat
// family ships, and refusing to see it would mean telling a Fedora or Rocky
// operator to install Docker from a third-party repository on a machine that
// already has a container runtime.
type Runtime struct {
	Kind    string // "docker", "podman", or "" when neither can be used
	Present bool   // one of them is installed, whether or not it works
	Usable  bool   // it answers *as this user*, which is the part that matters
	Version string
	Compose string // "v2" when compose works, "" otherwise
	Socket  string // for podman, the API socket compose is pointed at
	Why     string // why it is unusable, in a sentence fit to show someone

	// What is missing, named rather than described, so the caller does not
	// have to read Why to decide what to do. Reporting only the first thing
	// in the way meant fixing one of them and being told about the next.
	NeedsSocket  bool
	NeedsCompose bool
}

// Glibc is the C library a downloaded binary is linked against. The release
// binaries are built in a current Debian, and a host install on an older
// distribution would put a binary on the machine that cannot start.
type Glibc struct {
	Version string // "2.39"
	Major   int
	Minor   int
}

// AtLeast answers whether this glibc is new enough for a given version.
func (g Glibc) AtLeast(major, minor int) bool {
	if g.Major != major {
		return g.Major > major
	}
	return g.Minor >= minor
}

// Node is what a host install of the webmail would run on.
type Node struct {
	Present bool
	Version string // "22.14.0"
	Major   int
	Path    string // where it is; OwnPath when the installer put it there
	Why     string
}

// OwnPath is where the installer puts a Node of its own, deliberately outside
// PATH so it runs the webmail's unit and nothing else on the machine.
const OwnPath = "/opt/inbuxa/node/bin/node"

// Port is one of the ports the suite would like, and what holds it now.
type Port struct {
	Number  int
	For     string // what wants it
	Free    bool
	Unknown bool   // could not be tested: binding it needs privileges we lack
	Holder  string // best guess at the process, when we can see one
}

// wanted is every port the suite can ask for, with what asks. The survey
// reports all of them regardless of the shape chosen, because the interface
// needs to gray out a choice before the operator makes it.
var wanted = []struct {
	n    int
	for_ string
}{
	{25, "SMTP, mail from other servers"},
	{80, "HTTP, for certificates and the redirect"},
	{443, "HTTPS, the front ends and JMAP"},
	{465, "submissions, mail apps sending"},
	{993, "IMAPS, mail apps reading"},
	{995, "POP3S, mail apps collecting"},
	{4190, "ManageSieve, filters from mail apps"},
	{8080, "the webmail, behind the proxy"},
	{8081, "the server's plain HTTP, behind the proxy"},
}

// Survey takes the whole survey. It never fails: a fact it cannot establish
// is reported as absent with a reason, because "we could not tell" is itself
// something the operator should see.
func Survey(ctx context.Context) Facts {
	f := Facts{
		OS:       readOSRelease("/etc/os-release"),
		Systemd:  isSystemd(),
		Runtime:  surveyRuntime(ctx),
		Node:     surveyNode(ctx),
		Glibc:    surveyGlibc(ctx),
		MemoryMB: memoryMB(),
	}
	if u, err := user.Current(); err == nil {
		f.Root = u.Uid == "0"
	}
	for _, w := range wanted {
		f.Ports = append(f.Ports, surveyPort(w.n, w.for_))
	}
	f.DiskFreeGB = diskFreeGB("/var/lib")
	for _, p := range []string{"/etc/inbuxa/install.json"} {
		if _, err := os.Stat(p); err == nil {
			f.Existing = p
		}
	}
	return f
}

func readOSRelease(path string) OSInfo {
	var o OSInfo
	file, err := os.Open(path)
	if err != nil {
		return o
	}
	defer file.Close()
	fields := map[string]string{}
	s := bufio.NewScanner(file)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[k] = strings.Trim(v, `"'`)
	}
	o.ID = fields["ID"]
	o.VersionID = fields["VERSION_ID"]
	o.Pretty = fields["PRETTY_NAME"]
	like := fields["ID_LIKE"] + " " + o.ID
	switch {
	case strings.Contains(like, "debian"), strings.Contains(like, "ubuntu"):
		o.Family = "debian"
	case strings.Contains(like, "rhel"), strings.Contains(like, "fedora"), strings.Contains(like, "centos"):
		o.Family = "rhel"
	case strings.Contains(like, "arch"):
		o.Family = "arch"
	case strings.Contains(like, "suse"):
		o.Family = "suse"
	}
	return o
}

func isSystemd() bool {
	// /run/systemd/system exists exactly when systemd is running the machine,
	// which is what systemd's own documentation says to test.
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

// surveyRuntime looks for docker first and podman second, and reports the
// one that actually answers. Preferring docker where both exist keeps a
// machine that has been set up for docker working the way its operator
// expects; podman is what the Red Hat family ships, and is no less
// first-class for being second in the list.
func surveyRuntime(ctx context.Context) Runtime {
	if r := surveyDocker(ctx); r.Usable {
		return r
	} else if d := r; d.Present {
		// Docker is installed but broken. Podman may still be here and
		// working, and saying so is more use than reporting the broken one.
		if p := surveyPodman(ctx); p.Usable {
			return p
		}
		return d
	}
	if p := surveyPodman(ctx); p.Present {
		return p
	}
	return Runtime{Why: "neither docker nor podman is installed"}
}

func surveyDocker(ctx context.Context) Runtime {
	r := Runtime{Kind: "docker"}
	bin, err := exec.LookPath("docker")
	if err != nil {
		r.Kind = ""
		r.Why = "docker is not installed"
		return r
	}
	r.Present = true

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// `docker version` talks to the daemon, unlike `docker --version`, so it
	// answers the question that matters: can *this user* run containers?
	out, err := exec.CommandContext(ctx, bin, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		r.Why = firstLine(string(out))
		if r.Why == "" {
			r.Why = "the docker daemon did not answer"
		}
		return r
	}
	r.Usable = true
	r.Version = strings.TrimSpace(string(out))

	if err := exec.CommandContext(ctx, bin, "compose", "version").Run(); err == nil {
		r.Compose = "v2"
	} else {
		r.Usable = false
		r.NeedsCompose = true
		r.Why = "docker compose (v2) is not available"
	}
	return r
}

// surveyPodman reports podman and the API socket compose can be pointed at.
// Compose speaks the Docker API, and podman serves it, so the same compose
// plugin drives either -- which is why this installer does not carry two
// deployment paths for one compose file.
func surveyPodman(ctx context.Context) Runtime {
	r := Runtime{Kind: "podman"}
	bin, err := exec.LookPath("podman")
	if err != nil {
		r.Kind = ""
		r.Why = "podman is not installed"
		return r
	}
	r.Present = true

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "version", "--format", "{{.Version}}").CombinedOutput()
	if err != nil {
		r.Why = firstLine(string(out))
		if r.Why == "" {
			r.Why = "podman did not answer"
		}
		return r
	}
	r.Version = strings.TrimSpace(string(out))

	// Two things make compose work here, and both are checked before
	// reporting: the API socket podman.socket serves, which is not running
	// on a fresh machine, and the compose plugin itself.
	for _, path := range []string{"/run/podman/podman.sock"} {
		if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeSocket != 0 {
			r.Socket = path
		}
	}
	r.NeedsSocket = r.Socket == ""
	if _, err := os.Stat(ComposePluginPath); err == nil {
		r.Compose = "v2"
	} else {
		r.NeedsCompose = true
	}
	switch {
	case r.NeedsSocket && r.NeedsCompose:
		r.Why = "podman's API socket is not running (podman.socket), and the compose plugin is not installed"
	case r.NeedsSocket:
		r.Why = "podman's API socket is not running (podman.socket)"
	case r.NeedsCompose:
		r.Why = "the compose plugin is not installed"
	default:
		r.Usable = true
	}
	return r
}

// ComposePluginPath is where this installer puts the compose plugin, and
// where it looks for one it put there before. The same file serves docker
// and podman.
const ComposePluginPath = "/usr/local/lib/docker/cli-plugins/docker-compose"

// surveyGlibc reads the C library's version, which decides whether a
// downloaded binary can run here at all. The release binaries are built in a
// current Debian; an older distribution can host containers perfectly well
// and still be unable to start that file.
func surveyGlibc(ctx context.Context) Glibc {
	var g Glibc
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// getconf is in glibc itself; ldd --version is the fallback for a
	// machine where it is missing.
	out, err := exec.CommandContext(ctx, "getconf", "GNU_LIBC_VERSION").Output()
	if err != nil || len(out) == 0 {
		out, err = exec.CommandContext(ctx, "ldd", "--version").Output()
		if err != nil {
			return g
		}
	}
	m := regexp.MustCompile(`([0-9]+)\.([0-9]+)`).FindStringSubmatch(firstLine(string(out)))
	if m == nil {
		return g
	}
	g.Major, _ = strconv.Atoi(m[1])
	g.Minor, _ = strconv.Atoi(m[2])
	g.Version = m[1] + "." + m[2]
	return g
}

var nodeVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

func surveyNode(ctx context.Context) Node {
	var n Node
	// The installer's own Node first: it is deliberately not on PATH, so
	// looking only there would mean never seeing what we installed ourselves
	// and offering to install it again.
	bin := OwnPath
	if _, err := os.Stat(bin); err != nil {
		var err error
		bin, err = exec.LookPath("node")
		if err != nil {
			n.Why = "node is not installed"
			return n
		}
	}
	n.Present = true
	n.Path = bin
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		n.Why = "node is installed but would not run"
		return n
	}
	m := nodeVersion.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		n.Why = "node's version could not be read"
		return n
	}
	n.Version = strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	n.Major, _ = strconv.Atoi(m[1])
	return n
}

// surveyPort reports whether a port is free, and who has it when it is not.
// Binding is the only honest test -- something listening on 0.0.0.0 and
// something listening on one address are different answers, and /proc tells
// us the second only after the first has failed.
func surveyPort(n int, for_ string) Port {
	p := Port{Number: n, For: for_}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", n))
	if err == nil {
		l.Close()
		p.Free = true
		return p
	}
	// "I am not allowed to bind this" is not "something is listening here".
	// Reporting the first as the second told an unprivileged run that every
	// port below 1024 was taken, which is a lie an operator would act on.
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		p.Unknown = true
		if h := holderOf(n); h != "" {
			p.Holder = h
			p.Unknown = false
		}
		return p
	}
	p.Holder = holderOf(n)
	return p
}

// holderOf is a best effort at naming the process on a port, for the sake of
// a message like "443 is held by nginx" instead of "443 is busy". It reads
// /proc, which means it only sees the whole truth as root; a guess is better
// than nothing and the caller treats it as one.
func holderOf(port int) string {
	inodes := map[string]bool{}
	for _, tab := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(tab)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(file)
		s.Scan() // header
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) < 10 {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil || int(n) != port {
				continue
			}
			if fields[3] != "0A" { // 0A = LISTEN
				continue
			}
			inodes[fields[9]] = true
		}
		file.Close()
	}
	if len(inodes) == 0 {
		return ""
	}
	procs, err := filepath.Glob("/proc/[0-9]*/fd/*")
	if err != nil {
		return ""
	}
	for _, fd := range procs {
		link, err := os.Readlink(fd)
		if err != nil || !strings.HasPrefix(link, "socket:[") {
			continue
		}
		inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
		if !inodes[inode] {
			continue
		}
		pid := strings.Split(fd, "/")[2]
		if comm, err := os.ReadFile("/proc/" + pid + "/comm"); err == nil {
			return strings.TrimSpace(string(comm)) + " (pid " + pid + ")"
		}
	}
	return ""
}

func memoryMB() int {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	s := bufio.NewScanner(file)
	for s.Scan() {
		if !strings.HasPrefix(s.Text(), "MemTotal:") {
			continue
		}
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			return 0
		}
		kb, _ := strconv.Atoi(fields[1])
		return kb / 1024
	}
	return 0
}

func diskFreeGB(path string) int {
	out, err := exec.Command("df", "-BG", "--output=avail", path).Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(lines[1]), "G"))
	return n
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// PortsHeld returns the wanted ports that are not free, for the shapes that
// need them. An empty result is what lets an install proceed unasked.
func (f Facts) PortsHeld(numbers ...int) []Port {
	want := map[int]bool{}
	for _, n := range numbers {
		want[n] = true
	}
	var held []Port
	for _, p := range f.Ports {
		if want[p.Number] && !p.Free && !p.Unknown {
			held = append(held, p)
		}
	}
	return held
}

// Port returns one surveyed port by number.
func (f Facts) Port(n int) (Port, bool) {
	for _, p := range f.Ports {
		if p.Number == n {
			return p, true
		}
	}
	return Port{}, false
}
