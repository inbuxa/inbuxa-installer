// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command inbuxa installs the inbuxa suite on the machine it is run on.
//
// Run with no arguments it opens the interface; run with flags it does the
// same work without asking. The flags are not a second implementation: the
// interface fills them in with the machine's own facts in front of you.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/apply"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/discover"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/topology"
)

// version is stamped by the release build; a build from a working tree says
// so rather than claiming a release it is not.
var version = "dev"

const usage = `inbuxa -- install the inbuxa suite on this machine

  inbuxa install [flags]     install or converge (no flags: the interface)
  inbuxa survey              what this machine is, as the installer sees it
  inbuxa deps [--install]    what is missing for a shape, and fix it
  inbuxa plan -f FILE        what a topology file would change here
  inbuxa apply -f FILE       make this machine match that file
  inbuxa status              what is installed here, and whether it agrees
  inbuxa export [-o FILE]    write a topology file from what is here
  inbuxa version             this program's version

install flags:
  --domain NAME              the mail domain (required unless --local)
  --mail-host NAME           default: mail.DOMAIN
  --console-host NAME        default: admin.DOMAIN
  --webmail-host NAME        default: webmail.DOMAIN
  --email ADDRESS            ACME contact; default: postmaster@DOMAIN
  --server SHAPE             skip | container | host   (default: container)
  --console SHAPE            skip | container | host   (default: container)
  --webmail SHAPE            skip | container | host   (default: container)
  --proxy WHICH              caddy | snippets | none   (default: caddy)
  --dir PATH                 where the installation lives; default: /var/lib/inbuxa
  --local                    loopback evaluation: no public ports, no certificates
  --acme-directory URL       a private ACME CA, for testing the certificate path
  --acme-ca-root PATH        that CA's root, which both the server and the proxy
                             are made to trust
  --install-deps             install what the chosen shapes need and this
                             machine lacks, rather than refusing over it
  --dry-run                  print the plan and stop
  --yes                      do not ask for confirmation

deps flags:
  --server/--console/--webmail SHAPE   the shapes to work out the needs for
  --install                  do it, rather than only saying what it would do
`

func main() {
	// The three programs are Linux services: systemd units, a container
	// runtime, /etc and /var. This binary compiles for macOS and Windows
	// because Go will compile it for anything, and on either it would read
	// no /etc/os-release, find no systemd, and report a machine that does
	// not exist. Saying so is the only honest thing it can do there.
	if runtime.GOOS != "linux" {
		fmt.Fprintf(os.Stderr,
			"inbuxa installs on Linux, and this is %s.\n\n"+
				"The mail server, the console and the webmail are Linux services. To try them\n"+
				"on this machine, run them in containers with Docker or Podman Desktop; to\n"+
				"install them, run this on the Linux machine that will host them.\n",
			runtime.GOOS)
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		// The interface is the no-argument case. Until it lands, say so
		// plainly rather than pretending: a half-built screen is worse than
		// a sentence telling you which flags to use.
		fmt.Fprint(os.Stderr, "the interface is not built yet -- run `inbuxa install --help` for the flags\n")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "install":
		os.Exit(install(os.Args[2:]))
	case "survey":
		os.Exit(survey())
	case "deps":
		os.Exit(depsCmd(os.Args[2:]))
	case "plan":
		os.Exit(topologyCmd(os.Args[2:], false))
	case "apply":
		os.Exit(topologyCmd(os.Args[2:], true))
	case "export":
		os.Exit(exportCmd(os.Args[2:]))
	case "status":
		os.Exit(statusCmd())
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func survey() int {
	f := host.Survey(context.Background())
	fmt.Print(render(f))
	return 0
}

func install(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		o       plan.Options
		server  = fs.String("server", "container", "")
		console = fs.String("console", "container", "")
		webmail = fs.String("webmail", "container", "")
		dryRun  = fs.Bool("dry-run", false, "")
		yes     = fs.Bool("yes", false, "")
	)
	fs.StringVar(&o.Domain, "domain", "", "")
	fs.StringVar(&o.MailHost, "mail-host", "", "")
	fs.StringVar(&o.ConsoleHost, "console-host", "", "")
	fs.StringVar(&o.WebmailHost, "webmail-host", "", "")
	fs.StringVar(&o.ACMEEmail, "email", "", "")
	fs.StringVar(&o.Proxy, "proxy", "", "")
	fs.StringVar(&o.Dir, "dir", "", "")
	fs.BoolVar(&o.Local, "local", false, "")
	fs.BoolVar(&o.InstallDeps, "install-deps", false, "")
	fs.StringVar(&o.ACMEDirectory, "acme-directory", "", "")
	fs.StringVar(&o.ACMECARoot, "acme-ca-root", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for _, c := range []struct {
		comp plan.Component
		val  string
	}{{plan.Server, *server}, {plan.Console, *console}, {plan.Webmail, *webmail}} {
		s, err := shape(c.val)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--%s: %v\n", c.comp, err)
			return 2
		}
		o.Choices = append(o.Choices, plan.Choice{Component: c.comp, Shape: s})
	}

	f := host.Survey(context.Background())
	p, err := plan.Build(f, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot install as asked: "+err.Error())
		fmt.Fprint(os.Stderr, "\n"+render(f))
		return 1
	}
	fmt.Print(p.String())
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}

	fmt.Println("\nApplying:")
	log := &printer{}
	res, err := apply.Run(context.Background(), p, f, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nChecking it works:")
	problems := apply.Verify(context.Background(), res,
		o.Shape(plan.Console) != plan.Skip, o.Shape(plan.Webmail) != plan.Skip, log)
	for _, why := range problems {
		fmt.Fprintln(os.Stderr, "  problem: "+why)
	}

	fmt.Printf("\nDone. %s\n", res.Dir)
	fmt.Printf("  administrator  %s\n", res.AdminUser)
	if res.FirstUser != "" {
		fmt.Printf("  first mailbox  %s\n", res.FirstUser)
	}
	fmt.Printf("  passwords      %s\n", filepath.Join(res.Dir, "credentials.txt"))
	if res.ConsoleURL != "" {
		fmt.Printf("  console        %s\n", res.ConsoleURL)
	}
	if res.WebmailURL != "" {
		fmt.Printf("  webmail        %s\n", res.WebmailURL)
	}
	if res.Certificate != "" {
		fmt.Printf("  certificate    issued by %s\n", res.Certificate)
	}
	if res.ZoneFile != "" {
		fmt.Printf("  dns records    %s\n", res.ZoneFile)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// printer is apply's log on the flag path: steps as lines, everything a
// command says indented under the step that ran it.
type printer struct{}

func (printer) Step(format string, a ...any) { fmt.Printf("  "+format+"\n", a...) }
func (printer) Info(format string, a ...any) { fmt.Printf("      "+format+"\n", a...) }
func (printer) Out() io.Writer               { return os.Stdout }

// depsCmd is the offer on its own: what the chosen shapes need that this
// machine does not have, and -- with --install -- the doing of it. It exists
// separately from install because an operator preparing a machine should be
// able to get it ready without being asked for a domain first.
func depsCmd(args []string) int {
	fs := flag.NewFlagSet("deps", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		server  = fs.String("server", "container", "")
		console = fs.String("console", "container", "")
		webmail = fs.String("webmail", "container", "")
		doIt    = fs.Bool("install", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wantContainers, wantHostWebmail := false, false
	for _, c := range []struct {
		comp plan.Component
		val  string
	}{{plan.Server, *server}, {plan.Console, *console}, {plan.Webmail, *webmail}} {
		sh, err := shape(c.val)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--%s: %v\n", c.comp, err)
			return 2
		}
		if sh == plan.Container {
			wantContainers = true
		}
		if sh == plan.Host && c.comp == plan.Webmail {
			wantHostWebmail = true
		}
	}

	ctx := context.Background()
	f := host.Survey(ctx)
	needs := deps.For(f, wantContainers, wantHostWebmail)
	if len(needs) == 0 {
		fmt.Println("Nothing is missing for those shapes.")
		return 0
	}
	fmt.Println("Missing, for the shapes asked about:")
	fmt.Print(deps.Describe(needs))
	if !deps.Fixable(needs) {
		return 1
	}
	if !*doIt {
		fmt.Println("\nPass --install to do it.")
		return 0
	}
	if !f.Root {
		fmt.Fprintln(os.Stderr, "\ninstalling this needs root")
		return 1
	}
	fmt.Println("\nInstalling:")
	if err := deps.Resolve(ctx, os.Stdout, needs); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	after := host.Survey(ctx)
	fmt.Println("\nNow:")
	fmt.Print(render(after))
	return 0
}

// topologyCmd is plan and apply: the same reading of the same file, one of
// which stops after printing.
//
// A machine acts on its own part of the file and prints what the others have
// to run. It never reaches them -- the file is copied across by whoever owns
// those machines, and run there. That is the whole security posture of the
// designer this is the executor for: emit, never execute.
func topologyCmd(args []string, doIt bool) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		file        = fs.String("f", "", "")
		machineName = fs.String("machine", "", "")
		yes         = fs.Bool("yes", false, "")
		installDeps = fs.Bool("install-deps", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "which file? pass -f topology.json")
		return 2
	}
	t, err := topology.Load(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}

	name := *machineName
	if name == "" {
		h, _ := os.Hostname()
		name = h
	}
	m, ok := t.Machine(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "this machine is %q, which the file does not mention.\n", name)
		fmt.Fprintf(os.Stderr, "It describes: %s\n", strings.Join(machineNames(t), ", "))
		fmt.Fprintln(os.Stderr, "Pass --machine to say which one this is.")
		return 1
	}

	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	d := topology.Compare(st, m)
	fmt.Print(d.String())
	fmt.Print(topology.Elsewhere(t, *file, name))

	if !doIt {
		return 0
	}
	if d.Empty() {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}

	// Removals are the half that can lose something. Naming them again here,
	// after the diff and before the work, is the last chance to read them.
	if rm := d.Removals(); len(rm) > 0 {
		fmt.Println()
		for _, c := range rm {
			fmt.Printf("  removing %s from this machine; its data volumes are left in place\n", c.Component)
		}
	}

	o := plan.Options{
		Domain: t.Domain, MailHost: t.MailHost, ConsoleHost: t.ConsoleHost,
		WebmailHost: t.WebmailHost, ACMEEmail: t.ACMEEmail,
		Dir: m.Dir, Proxy: m.Proxy, InstallDeps: *installDeps,
		Machine: name, TopologyPath: *file,
	}
	for _, kind := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		sh := plan.Skip
		if s := m.Shape(string(kind)); s != "" {
			sh = plan.Shape(s)
		}
		o.Choices = append(o.Choices, plan.Choice{Component: kind, Shape: sh})
	}

	f := host.Survey(context.Background())
	p, err := plan.Build(f, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\ncannot apply this file here: "+err.Error())
		return 1
	}
	fmt.Println("\nApplying:")
	log := &printer{}
	res, err := apply.Run(context.Background(), p, f, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Printf("\nDone. %s\n", res.Dir)
	if res.AdminUser != "" {
		fmt.Printf("  administrator  %s\n", res.AdminUser)
	}
	return 0
}

// exportCmd writes what is on this machine as a topology file, so a design
// starts from a system that exists rather than a blank page.
func exportCmd(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	out := fs.String("o", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	f := host.Survey(ctx)
	found := discover.Run(ctx, f)

	if !st.Installed() && found.Dir == "" {
		fmt.Fprintln(os.Stderr, "nothing is installed here, so there is nothing to describe")
		return 1
	}

	name := st.Machine
	if name == "" {
		name, _ = os.Hostname()
	}
	dir, domain := st.Dir, st.Domain
	if dir == "" {
		dir = found.Dir
	}
	if domain == "" {
		domain = found.Domain
	}

	// Intent first, because it knows the shapes; reality for anything intent
	// does not mention, so a deployment made by an older version or by hand
	// still describes itself.
	m := topology.Machine{Name: name, Dir: dir}
	for _, kind := range []string{"server", "console", "webmail"} {
		switch {
		case st.Shapes[kind] != "":
			m.Components = append(m.Components, topology.Component{Kind: kind, Shape: st.Shapes[kind]})
		case found.Services[kind] != "":
			m.Components = append(m.Components, topology.Component{Kind: kind, Shape: "container"})
		}
	}
	if !found.Proxy && found.Dir != "" {
		m.Proxy = "none"
	}
	t := &topology.Topology{Version: topology.Version, Domain: domain, Machines: []topology.Machine{m}}
	if found.MailHost != "" {
		t.MailHost = found.MailHost
	}
	t.Defaults()
	if err := t.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "what is installed here does not describe a whole installation: "+err.Error())
		fmt.Fprintln(os.Stderr, "(a machine running only front ends is one part of a file, not all of it)")
		return 1
	}
	if *out == "" {
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	if err := t.Save(*out); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Printf("wrote %s\n", *out)
	return 0
}

// statusCmd is the same reading as export, shown to a person: what is
// installed, what is running, and where the two disagree.
func statusCmd() int {
	ctx := context.Background()
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	f := host.Survey(ctx)
	found := discover.Run(ctx, f)

	if !st.Installed() && found.Dir == "" {
		fmt.Println("Nothing is installed on this machine.")
		return 0
	}

	name := st.Machine
	if name == "" {
		name, _ = os.Hostname()
	}
	fmt.Printf("This machine is %q", name)
	if st.Topology != "" {
		fmt.Printf(", from %s", st.Topology)
	}
	fmt.Println()
	if found.Dir != "" {
		fmt.Printf("  %-14s %s\n", "deployment", found.Dir)
	}
	if found.Domain != "" || st.Domain != "" {
		domain := st.Domain
		if domain == "" {
			domain = found.Domain
		}
		fmt.Printf("  %-14s %s\n", "domain", domain)
	}
	if found.Runtime != "" {
		fmt.Printf("  %-14s %s\n", "runtime", found.Runtime)
	}

	fmt.Println("\nComponents")
	for _, kind := range []string{"server", "console", "webmail"} {
		shape, intended := st.Shapes[kind]
		state, present := found.Services[kind]
		switch {
		case !intended && !present:
			continue
		case intended && present:
			fmt.Printf("  %-8s installed as a %-9s %s\n", kind, shape, state)
		case intended:
			fmt.Printf("  %-8s installed as a %-9s missing from the deployment\n", kind, shape)
		default:
			fmt.Printf("  %-8s %-24s %s\n", kind, "in the deployment", state)
		}
	}

	if d := discover.Drift(st.Shapes, found); len(d) > 0 {
		fmt.Println("\nWorth a look")
		for _, line := range d {
			fmt.Printf("  - %s\n", line)
		}
		return 1
	}
	return 0
}

func machineNames(t *topology.Topology) []string {
	var out []string
	for _, m := range t.Machines {
		out = append(out, m.Name)
	}
	return out
}

func shape(s string) (plan.Shape, error) {
	switch strings.ToLower(s) {
	case "skip", "no", "none":
		return plan.Skip, nil
	case "container", "docker":
		return plan.Container, nil
	case "host", "bare", "baremetal", "bare-metal":
		return plan.Host, nil
	}
	return "", fmt.Errorf("%q is not a shape: skip, container or host", s)
}

// render is the first screen, as text: what this machine is, and what that
// allows. Every line is something a later choice depends on.
func render(f host.Facts) string {
	var b strings.Builder
	b.WriteString("This machine\n")
	os_ := f.OS.Pretty
	if os_ == "" {
		os_ = "an unrecognized Linux"
	}
	fmt.Fprintf(&b, "  %-22s %s\n", "system", os_)
	fmt.Fprintf(&b, "  %-22s %s\n", "init", yes(f.Systemd, "systemd", "not systemd -- host installs are unavailable"))
	fmt.Fprintf(&b, "  %-22s %s\n", "running as", yes(f.Root, "root", "an ordinary user -- installing will fail"))
	if f.MemoryMB > 0 {
		fmt.Fprintf(&b, "  %-22s %d MB\n", "memory", f.MemoryMB)
	}
	if f.DiskFreeGB > 0 {
		fmt.Fprintf(&b, "  %-22s %d GB free\n", "disk", f.DiskFreeGB)
	}

	runtime := "none: neither docker nor podman is installed"
	switch {
	case f.Runtime.Usable:
		runtime = f.Runtime.Kind + " " + f.Runtime.Version + ", compose " + f.Runtime.Compose
		if f.Runtime.Socket != "" {
			runtime += ", at " + f.Runtime.Socket
		}
	case f.Runtime.Present:
		runtime = f.Runtime.Kind + " installed but not usable: " + f.Runtime.Why
	}
	fmt.Fprintf(&b, "  %-22s %s\n", "container runtime", runtime)

	glibc := "could not be read"
	if f.Glibc.Version != "" {
		glibc = f.Glibc.Version
		if !f.Glibc.AtLeast(plan.ServerGlibcMajor, plan.ServerGlibcMinor) {
			glibc += fmt.Sprintf(" (older than the %d.%d the server binary needs)", plan.ServerGlibcMajor, plan.ServerGlibcMinor)
		}
	}
	fmt.Fprintf(&b, "  %-22s %s\n", "glibc", glibc)

	node := "not installed"
	switch {
	case f.Node.Present && f.Node.Major >= 22:
		node = f.Node.Version + " at " + f.Node.Path
	case f.Node.Present:
		node = f.Node.Version + " (too old for a host install of the webmail)"
	}
	fmt.Fprintf(&b, "  %-22s %s\n", "node", node)
	if f.Existing != "" {
		fmt.Fprintf(&b, "  %-22s %s\n", "existing install", f.Existing)
	}

	b.WriteString("\nPorts\n")
	for _, p := range f.Ports {
		state := "free"
		switch {
		case p.Free:
		case p.Unknown:
			state = "cannot tell without root"
		default:
			state = "held"
			if p.Holder != "" {
				state += " by " + p.Holder
			}
		}
		fmt.Fprintf(&b, "  %-6d %-42s %s\n", p.Number, p.For, state)
	}

	b.WriteString("\nWhat this machine allows\n")
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		var shapes []string
		for _, s := range []plan.Shape{plan.Container, plan.Host} {
			if a := plan.Available(f, c, s); a.OK {
				shapes = append(shapes, string(s))
			} else {
				shapes = append(shapes, fmt.Sprintf("%s (no: %s)", s, a.Why))
			}
		}
		fmt.Fprintf(&b, "  %-10s %s\n", c, strings.Join(shapes, "   "))
	}
	return b.String()
}

func yes(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
