// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command inbuxa installs the inbuxa suite on the machine it is run on.
//
// Run with no arguments it opens the interface; run with flags it does the
// same work without asking. The flags are not a second implementation: the
// interface fills them in with the machine's own facts in front of you.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/apply"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/cluster"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/discover"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/doctor"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/relay"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/topology"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/tui"
)

// version is stamped by the release build; a build from a working tree says
// so rather than claiming a release it is not.
var version = "dev"

const usage = `inbuxa -- install the inbuxa suite on this machine

  inbuxa                     the interface: the same as install, with the
                             machine's own facts on screen
  inbuxa install [flags]     install or converge (no flags: the interface)
  inbuxa join URL [flags]    the console and the webmail here, for a server
                             already running at URL
  inbuxa survey              what this machine is, as the installer sees it
  inbuxa deps [--install]    what is missing for a shape, and fix it
  inbuxa plan -f FILE        what a topology file would change here
  inbuxa apply -f FILE       make this machine match that file
  inbuxa status              what is installed here, and whether it agrees
  inbuxa doctor              a read-only health check: services, the mail
                             server's certificate, the ports answering, and
                             the DNS a sender needs (exit 1 on a failure)
  inbuxa upgrade [component…]  move what is here to the releases this
                             installer installs: --dry-run, --yes,
                             --no-backup, --check (report only; exit 10 if
                             anything can be upgraded)
  inbuxa uninstall           take the programs away, keep the data:
                             --dry-run, --yes, --purge (the data too)
  inbuxa certs [--key KEY]   the mail server's certificate: what its mail
                             ports present, and with an API key a new
                             order when it needs one
  inbuxa dns                 the DNS records the domain needs: the saved
                             dns.zone, or --refresh --key - to re-read them
                             live from the server
  inbuxa export [-o FILE]    write a topology file from what is here
  inbuxa relay install|status|uninstall
                             inbuxa Relay, on a machine of its own in front
                             of the server
  inbuxa cluster doctor [flags]
                             whether this node can reach a cluster's data
                             layer (PostgreSQL, NATS, etcd, Patroni, S3)
  inbuxa cluster join -f FILE
                             point this node's server at the shared store in
                             the file's cluster block and give it its role
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
  --key KEY                  an API key made in the console, needed only to
                             move the server between a container and the host
                             (run install again with the other --server), and
                             to move a --proxy snippets install made before
                             nginx passed the server's names through to it;
                             "-" reads it from standard input, and
                             INBUXA_API_KEY works too
  --answers FILE             read these options from a JSON file, for an
                             unattended install (cloud-init, Ansible); keys
                             are the flag names, and a flag given here still
                             wins over the file
  --dry-run                  print the plan and stop
  --yes                      do not ask for confirmation

join flags:
  --key KEY                  an API key made in the console by a server
                             administrator; "-" reads it from standard input.
                             INBUXA_JOIN_KEY works too, and keeps it out of
                             the shell's history
  --console SHAPE            container | host | skip   (default: container)
  --webmail SHAPE            container | host | skip   (default: container)
  --domain NAME              default: the server's name without its first label
  --console-host, --webmail-host, --email, --proxy, --dir, --local,
  --install-deps, --dry-run, --yes   as for install
  --replace                  put these front ends in place of others the
                             server already lists
  --exempt-address IP        the address the server sees the webmail at, when
                             it is not what the webmail's name resolves to

certs flags:
  --key KEY                  an API key made in the console by a server
                             administrator, needed only to change anything;
                             "-" reads it from standard input, and
                             INBUXA_API_KEY works too
  --mail-host NAME           the server's name, if the install didn't record it
  --email ADDRESS            the ACME contact, if ACME is turned on here;
                             default: postmaster@DOMAIN
  --dry-run                  report, and say what it would do

dns flags:
  --refresh                  read the records live from the server instead of
                             the saved dns.zone; needs an API key
  --key KEY                  an API key made in the console, needed only with
                             --refresh; "-" reads it from standard input, and
                             INBUXA_API_KEY works too
  --domain NAME              the mail domain, if the install didn't record it

relay install flags:
  --token TOKEN              the join token the console shows when you add
                             the relay; "-" reads it from standard input, and
                             INBUXA_RELAY_JOIN_TOKEN works too. Not needed
                             once it has joined
  --link-listen ADDR         where the server's link reaches it; default
                             [::]:443, or what an earlier run set
  --dry-run, --yes           as for install

relay uninstall flags:
  --purge                    its state too: it leaves its server for good
  --discard-held             with --purge, even while it holds mail
  --dry-run, --yes           as for install

cluster doctor flags (each endpoint is HOST:PORT; only the ones given are
checked, so it fits any deployment; omit them all and it does nothing):
  --pg HOST:PORT             the PostgreSQL store (often an HAProxy listener)
  --nats HOST:PORT           the NATS coordinator
  --etcd HOST:PORT           etcd, checked at http://HOST:PORT/health
  --patroni HOST:PORT        Patroni, checked at /primary (200 = the leader)
  --s3 HOST:PORT             the S3 blob store, checked at /health
  --timeout DURATION         how long each probe waits; default 5s

cluster join flags:
  -f FILE                    the version 2 topology file; this machine's
                             cluster block says where its store, coordinator
                             and blob store are and what role it takes
  --machine NAME             which machine in the file this is; default: the
                             hostname
  --dry-run                  print what it would write and stop
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
	if len(os.Args) < 2 || (os.Args[1] == "install" && len(os.Args) == 2) {
		os.Exit(interactive())
	}
	switch os.Args[1] {
	case "install":
		os.Exit(install(os.Args[2:]))
	case "join":
		os.Exit(join(os.Args[2:]))
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
	case "doctor":
		os.Exit(doctorCmd(os.Args[2:]))
	case "upgrade":
		os.Exit(upgradeCmd(os.Args[2:]))
	case "uninstall":
		os.Exit(uninstallCmd(os.Args[2:]))
	case "certs":
		os.Exit(certsCmd(os.Args[2:]))
	case "dns":
		os.Exit(dnsCmd(os.Args[2:]))
	case "relay":
		os.Exit(relayCmd(os.Args[2:]))
	case "cluster":
		os.Exit(clusterCmd(os.Args[2:]))
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

// answersFile carries install options from a file, for an unattended install
// driven by cloud-init, Ansible or a kickstart rather than a long command
// line. Its keys are the install flags. Every field is a pointer, so a key
// left out keeps the default and an explicit `"local": false` is told apart
// from an absent one. A command-line flag always overrides the file.
type answersFile struct {
	Domain        *string `json:"domain"`
	MailHost      *string `json:"mail-host"`
	ConsoleHost   *string `json:"console-host"`
	WebmailHost   *string `json:"webmail-host"`
	Email         *string `json:"email"`
	Proxy         *string `json:"proxy"`
	Dir           *string `json:"dir"`
	Local         *bool   `json:"local"`
	InstallDeps   *bool   `json:"install-deps"`
	ACMEDirectory *string `json:"acme-directory"`
	ACMECARoot    *string `json:"acme-ca-root"`
	Server        *string `json:"server"`
	Console       *string `json:"console"`
	Webmail       *string `json:"webmail"`
}

// parseAnswers reads an answers file as JSON, rejecting unknown keys so a
// mistyped field name is caught rather than quietly ignored.
func parseAnswers(b []byte) (answersFile, error) {
	var a answersFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return answersFile{}, err
	}
	return a, nil
}

// applyAnswers fills the options the command line did not set (those are in
// set) from the answers file; a flag given on the command line always wins.
func applyAnswers(a answersFile, set map[string]bool, o *plan.Options, server, console, webmail *string) {
	str := func(name string, dst, v *string) {
		if v != nil && !set[name] {
			*dst = *v
		}
	}
	boolean := func(name string, dst, v *bool) {
		if v != nil && !set[name] {
			*dst = *v
		}
	}
	str("domain", &o.Domain, a.Domain)
	str("mail-host", &o.MailHost, a.MailHost)
	str("console-host", &o.ConsoleHost, a.ConsoleHost)
	str("webmail-host", &o.WebmailHost, a.WebmailHost)
	str("email", &o.ACMEEmail, a.Email)
	str("proxy", &o.Proxy, a.Proxy)
	str("dir", &o.Dir, a.Dir)
	str("acme-directory", &o.ACMEDirectory, a.ACMEDirectory)
	str("acme-ca-root", &o.ACMECARoot, a.ACMECARoot)
	str("server", server, a.Server)
	str("console", console, a.Console)
	str("webmail", webmail, a.Webmail)
	boolean("local", &o.Local, a.Local)
	boolean("install-deps", &o.InstallDeps, a.InstallDeps)
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
		key     = fs.String("key", os.Getenv("INBUXA_API_KEY"), "")
		answers = fs.String("answers", "", "")
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
	if *answers != "" {
		set := map[string]bool{}
		fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
		b, err := os.ReadFile(*answers)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reading --answers: "+err.Error())
			return 2
		}
		a, err := parseAnswers(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--answers %s: %v\n", *answers, err)
			return 2
		}
		applyAnswers(a, set, &o, server, console, webmail)
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
	moving := false
	if st, err := state.Load(); err == nil {
		if from, ok := apply.MovingServer(st, p.Options); ok {
			moving = true
			fmt.Print("\n" + apply.MoveNote(from, p.Options.Shape(plan.Server)))
		}
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}
	if moving || *key != "" {
		k, err := readKey(*key)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		p.Options.Key = k
	}

	fmt.Println("\nApplying:")
	log := &printer{}
	res, err := apply.Run(context.Background(), p, f, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nChecking it works:")
	problems := apply.Verify(context.Background(), res, p.Options, true, log)
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
	if res.ProxyConfig != "" {
		fmt.Printf("  proxy config   %s (load it into your web server)\n", res.ProxyConfig)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// join installs the front ends here for a server elsewhere. The server is
// reached only over its own HTTPS, with an API key, never over SSH.
func join(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "which server? inbuxa join https://mail.example.com --key ...")
		return 2
	}
	serverURL := strings.TrimRight(args[0], "/")
	if !strings.HasPrefix(serverURL, "https://") && !strings.HasPrefix(serverURL, "http://") {
		serverURL = "https://" + serverURL
	}
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		o       plan.Options
		key     = fs.String("key", os.Getenv("INBUXA_JOIN_KEY"), "")
		console = fs.String("console", "container", "")
		webmail = fs.String("webmail", "container", "")
		dryRun  = fs.Bool("dry-run", false, "")
		yes     = fs.Bool("yes", false, "")
	)
	fs.StringVar(&o.Domain, "domain", "", "")
	fs.StringVar(&o.ConsoleHost, "console-host", "", "")
	fs.StringVar(&o.WebmailHost, "webmail-host", "", "")
	fs.StringVar(&o.ACMEEmail, "email", "", "")
	fs.StringVar(&o.Proxy, "proxy", "", "")
	fs.StringVar(&o.Dir, "dir", "", "")
	fs.BoolVar(&o.Local, "local", false, "")
	fs.BoolVar(&o.InstallDeps, "install-deps", false, "")
	fs.BoolVar(&o.Replace, "replace", false, "")
	fs.StringVar(&o.ExemptAddress, "exempt-address", "", "")
	fs.StringVar(&o.ACMEDirectory, "acme-directory", "", "")
	fs.StringVar(&o.ACMECARoot, "acme-ca-root", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *key == "-" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "reading the key from standard input: "+err.Error())
			return 2
		}
		*key = strings.TrimSpace(line)
	}
	if *key == "" {
		fmt.Fprintln(os.Stderr, "an API key is needed: make one in the console (your account, API keys) and pass it with --key or INBUXA_JOIN_KEY")
		return 2
	}
	o.ServerURL = serverURL
	if o.Domain == "" {
		host := strings.TrimPrefix(strings.TrimPrefix(serverURL, "https://"), "http://")
		host = strings.SplitN(strings.SplitN(host, "/", 2)[0], ":", 2)[0]
		// A name gives its domain; an address gives none, and --domain
		// (or --local) is needed then.
		if net.ParseIP(host) == nil {
			if _, rest, ok := strings.Cut(host, "."); ok && strings.Contains(rest, ".") {
				o.Domain = rest
			} else {
				o.Domain = host
			}
		}
	}
	o.Choices = append(o.Choices, plan.Choice{Component: plan.Server, Shape: plan.Skip})
	for _, c := range []struct {
		comp plan.Component
		val  string
	}{{plan.Console, *console}, {plan.Webmail, *webmail}} {
		s, err := shape(c.val)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--%s: %v\n", c.comp, err)
			return 2
		}
		o.Choices = append(o.Choices, plan.Choice{Component: c.comp, Shape: s})
	}

	ctx := context.Background()
	f := host.Survey(ctx)
	p, err := plan.Build(f, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot join as asked: "+err.Error())
		return 1
	}
	fmt.Print(p.String())
	// The server is asked even on a dry run: it changes nothing, and a key
	// that doesn't work or a front end it would replace is worth knowing
	// before anything else.
	fmt.Printf("\nAsking %s:\n", serverURL)
	if err := apply.JoinCheck(ctx, p.Options, *key); err != nil {
		fmt.Fprintln(os.Stderr, "  "+err.Error())
		return 1
	}
	fmt.Println("  the key works, and the server can take these front ends")
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}

	fmt.Println("\nApplying:")
	log := &printer{}
	res, err := apply.Join(ctx, p, f, *key, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nChecking it works:")
	problems := apply.JoinVerify(ctx, res, p.Options.Shape(plan.Console), p.Options.Shape(plan.Webmail), log)
	for _, why := range problems {
		fmt.Fprintln(os.Stderr, "  problem: "+why)
	}
	fmt.Printf("\nDone. %s, joined to %s\n", res.Dir, serverURL)
	if res.ConsoleURL != "" {
		fmt.Printf("  console        %s\n", res.ConsoleURL)
	}
	if res.WebmailURL != "" {
		fmt.Printf("  webmail        %s\n", res.WebmailURL)
	}
	fmt.Println("  The API key is not kept here; you can remove it in the console now.")
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// interactive is the no-argument case: the interface, when there is a
// terminal to show it on. Without one -- a pipe, a CI job -- it says which
// flags do the same, rather than waiting on keys that will never come.
func interactive() int {
	if !tui.IsTerminal(os.Stdin) || !tui.IsTerminal(os.Stdout) {
		fmt.Fprint(os.Stderr, "no terminal to show the installer in -- run `inbuxa install --help` for the flags that do the same\n")
		return 2
	}
	w := tui.NewWizard(host.Survey(context.Background()), installer{})
	if err := tui.Run(w, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	summary, ran, ok := w.Outcome()
	if !ran {
		return 0
	}
	fmt.Println(strings.Join(summary, "\n"))
	if !ok {
		return 1
	}
	return 0
}

// installer is what the interface installs with: the same apply.Run and
// apply.Verify the flags use.
type installer struct{}

func (installer) Run(ctx context.Context, p plan.Plan, f host.Facts, log apply.Log) (*apply.Result, error) {
	return apply.Run(ctx, p, f, log)
}

func (installer) JoinCheck(ctx context.Context, o plan.Options, key string) error {
	return apply.JoinCheck(ctx, o, key)
}

func (installer) Join(ctx context.Context, p plan.Plan, f host.Facts, key string, log apply.Log) (*apply.Result, error) {
	return apply.Join(ctx, p, f, key, log)
}

func (installer) JoinVerify(ctx context.Context, res *apply.Result, console, webmail plan.Shape, log apply.Log) []string {
	return apply.JoinVerify(ctx, res, console, webmail, log)
}

func (installer) Verify(ctx context.Context, res *apply.Result, o plan.Options, log apply.Log) []string {
	return apply.Verify(ctx, res, o, true, log)
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
		key         = fs.String("key", os.Getenv("INBUXA_API_KEY"), "")
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
	if from, ok := apply.MovingServer(st, p.Options); ok {
		fmt.Print("\n" + apply.MoveNote(from, p.Options.Shape(plan.Server)))
		k, err := readKey(*key)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		p.Options.Key = k
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
		case (kind == "server" && found.HostServer) || (kind == "console" && found.HostConsole) || (kind == "webmail" && found.HostWebmail):
			m.Components = append(m.Components, topology.Component{Kind: kind, Shape: "host"})
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

// upgradeCmd moves what is installed here to the releases this installer
// pins: the server first, each checked and put back if it fails, the
// server's data archived before it unless --no-backup says not to.
func upgradeCmd(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	dryRun := fs.Bool("dry-run", false, "")
	yes := fs.Bool("yes", false, "")
	noBackup := fs.Bool("no-backup", false, "")
	check := fs.Bool("check", false, "")
	// Components and options in any order: `upgrade server --yes` as well as
	// `upgrade --yes server`. The flag package stops at the first argument
	// that isn't an option, so parse again after each one.
	var names []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		names = append(names, fs.Arg(0))
		args = fs.Args()[1:]
	}
	want := map[string]bool{}
	for _, a := range names {
		c, err := shapeComponent(a)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		want[c] = true
	}
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if !st.Installed() || st.Removed() {
		fmt.Fprintln(os.Stderr, "nothing is installed here to upgrade")
		return 1
	}
	var steps []apply.Step
	for _, s := range apply.UpgradePlan(st) {
		if len(want) == 0 || want[string(s.Component)] {
			steps = append(steps, s)
		}
	}
	due := 0
	fmt.Printf("Upgrade with installer %s\n\n", version)
	for _, s := range steps {
		mark := "already " + s.To
		if s.Due() {
			mark = s.From + " -> " + s.To
			due++
		}
		fmt.Printf("  %-8s %-10s %s\n", s.Component, s.Shape, mark)
	}
	if due == 0 {
		fmt.Println("\nNothing to do: this is what this installer installs. A newer installer brings newer releases.")
		return 0
	}
	// --check reports and stops, with an exit code a script or a cron job can
	// act on: 10 when something can be upgraded, 0 when nothing can. It never
	// changes anything, so it needs neither --yes nor root.
	if *check {
		fmt.Printf("\n%d component(s) can be upgraded. Run `inbuxa upgrade` to apply.\n", due)
		return 10
	}
	backup := !*noBackup
	for _, s := range steps {
		if s.Component == plan.Server && s.Due() {
			if backup {
				fmt.Printf("\nThe server is stopped and its data archived into %s first: a new release\nmay convert its store on first start, and that can't be undone.\n", filepath.Join(st.Dir, "backups"))
			} else {
				fmt.Println("\nThe server's data will not be archived first (--no-backup).")
			}
		}
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}
	f := host.Survey(context.Background())
	if !f.Root {
		fmt.Fprintln(os.Stderr, "upgrading needs root")
		return 1
	}
	fmt.Println("\nUpgrading:")
	if err := apply.RunUpgrade(context.Background(), st, steps, backup, f, &printer{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nDone.")
	return 0
}

// uninstallCmd takes away what the installer put here. The data stays
// unless --purge, and with it the install record, marked removed, so
// install run again brings everything back.
func uninstallCmd(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	dryRun := fs.Bool("dry-run", false, "")
	yes := fs.Bool("yes", false, "")
	purge := fs.Bool("purge", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		c := fs.Arg(0)
		fmt.Fprintf(os.Stderr, "uninstall takes away the whole installation. To take only the %s off this machine,\n"+
			"run install again as before, with --%s skip: it converges, and removes it.\n", c, c)
		return 2
	}
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if !st.Installed() || (st.Removed() && !*purge) {
		fmt.Println("Nothing is installed here to take away.")
		if st.Removed() {
			fmt.Printf("The data of an earlier install is kept in %s; --purge removes that too.\n", st.Dir)
		}
		return 0
	}
	r := apply.UninstallPlan(st, *purge)
	fmt.Println("Taken away:")
	for _, x := range r.Remove {
		fmt.Println("  - " + x)
	}
	if len(r.Keep) > 0 {
		fmt.Println("\nKept:")
		for _, x := range r.Keep {
			fmt.Println("  - " + x)
		}
	}
	if len(r.Notes) > 0 {
		fmt.Println("\nWorth knowing:")
		for _, x := range r.Notes {
			fmt.Println("  - " + x)
		}
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}
	f := host.Survey(context.Background())
	if !f.Root {
		fmt.Fprintln(os.Stderr, "uninstalling needs root")
		return 1
	}
	fmt.Println("\nUninstalling:")
	if err := apply.Uninstall(context.Background(), st, *purge, f, &printer{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nDone.")
	return 0
}

func certsCmd(args []string) int {
	fs := flag.NewFlagSet("certs", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		key      = fs.String("key", os.Getenv("INBUXA_API_KEY"), "")
		mailHost = fs.String("mail-host", "", "")
		email    = fs.String("email", "", "")
		acmeDir  = fs.String("acme-directory", "", "")
		dryRun   = fs.Bool("dry-run", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if _, ok := st.Shapes["server"]; !ok || st.Removed() {
		fmt.Println("The mail server isn't installed on this machine, so its certificate is looked after where it runs.")
		if st.ServerURL != "" {
			fmt.Println("The console's and the webmail's certificates here are Caddy's, which renews them itself.")
		}
		return 0
	}
	if apply.LocalInstall(st) {
		fmt.Println("This is a --local install: the mail ports are on loopback and have the server's own")
		fmt.Println("self-signed certificate, which is all a local evaluation needs. A real certificate comes")
		fmt.Println("with installing for a domain that points here.")
		return 0
	}
	f := host.Survey(ctx)
	found := discover.Run(ctx, f, st.Dir)
	o := apply.CertsOptions{
		MailHost: apply.CertsMailHost(*mailHost, st, found.MailHost),
		Email:    *email, ACMEDirectory: *acmeDir,
	}
	switch {
	case found.Proxy:
		o.Proxy = "caddy"
	case fileExists(filepath.Join(st.Dir, "proxy", "nginx-inbuxa.conf")):
		o.Proxy = "snippets"
	}

	roots := apply.CertRoots(st.Dir)
	s, err := apply.ReadServed(ctx, o.MailHost, roots)
	problem := ""
	if err != nil {
		problem = err.Error()
	} else {
		fmt.Printf("The mail server's certificate for %s, as port %s presents it:\n", o.MailHost, s.Port)
		issuer := s.Issuer
		if s.SelfSigned {
			issuer = "itself (self-signed)"
		}
		until := s.NotAfter.Format("2006-01-02")
		// A self-signed certificate is often dated to the end of time, where
		// a count of days says nothing.
		if days := int(time.Until(s.NotAfter).Hours() / 24); days < 3650 {
			until += fmt.Sprintf(" (%d days)", days)
		}
		fmt.Printf("  issued by    %s\n  valid until  %s\n  names        %s\n",
			issuer, until, strings.Join(s.Names, ", "))
		problem = s.Problem(o.MailHost, time.Now())
	}
	if addrs, err := net.DefaultResolver.LookupHost(ctx, o.MailHost); err == nil && len(addrs) > 0 {
		fmt.Printf("\n%s resolves to %s.\n", o.MailHost, strings.Join(addrs, ", "))
	} else {
		fmt.Printf("\n%s doesn't resolve. A certificate can't be issued until it points here: the records\n"+
			"are in %s.\n", o.MailHost, filepath.Join(st.Dir, "dns.zone"))
	}
	if problem == "" {
		fmt.Println("\nNothing to do: the server renews it itself, well before it expires.")
		return 0
	}
	fmt.Printf("\nThe problem: %s.\n", problem)
	if *dryRun {
		fmt.Println("\nWith an API key, this would start a new certificate order for " + o.MailHost + " and wait for it.")
		return 0
	}
	if *key == "-" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "reading the key from standard input: "+err.Error())
			return 2
		}
		*key = strings.TrimSpace(line)
	}
	if *key == "" {
		fmt.Println("\nA new order needs an API key: make one in the console (your account, API keys), then")
		fmt.Println("  sudo inbuxa certs --key -")
		fmt.Println("and paste it, or set INBUXA_API_KEY. It isn't kept.")
		return 1
	}
	fmt.Println()
	o.Key = *key
	if _, err := apply.Renew(ctx, st, o, &printer{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		if !errors.Is(err, apply.ErrKeyRefused) {
			fmt.Fprintln(os.Stderr, "    the server's log says why: "+apply.CertsLogHint(st))
		}
		return 1
	}
	s, _ = apply.ReadServed(ctx, o.MailHost, roots)
	if s != nil {
		fmt.Printf("\nDone: port %s now presents a certificate for %s from %s, valid until %s.\n",
			s.Port, o.MailHost, s.Issuer, s.NotAfter.Format("2006-01-02"))
	}
	return 0
}

// dnsCmd prints the DNS records the domain needs published. By default it
// reads the dns.zone the install wrote, which needs nothing; --refresh asks
// the running server for them live, with an API key, so a rotated DKIM key or
// a record that changed since the install shows up.
func dnsCmd(args []string) int {
	fs := flag.NewFlagSet("dns", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		refresh = fs.Bool("refresh", false, "")
		key     = fs.String("key", os.Getenv("INBUXA_API_KEY"), "")
		domain  = fs.String("domain", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if _, ok := st.Shapes["server"]; !ok || st.Removed() {
		fmt.Println("The mail server isn't installed on this machine, so its DNS records are shown where it runs.")
		return 0
	}
	if apply.LocalInstall(st) {
		fmt.Println("This is a --local install: the mail ports are on loopback and no DNS is needed. A real")
		fmt.Println("install for a domain that points here writes the records it needs to dns.zone.")
		return 0
	}
	dom := *domain
	if dom == "" {
		dom = st.Domain
	}
	zoneFile := filepath.Join(st.Dir, "dns.zone")

	// The saved file is the no-friction default: the install wrote it, it
	// needs no key, and for most domains it never goes stale.
	if !*refresh {
		b, err := os.ReadFile(zoneFile)
		if err == nil {
			fmt.Printf("The records %s needs published, from %s:\n\n", dom, zoneFile)
			os.Stdout.Write(b)
			if len(b) > 0 && b[len(b)-1] != '\n' {
				fmt.Println()
			}
			fmt.Println("\nTo re-read them live from the server (a rotated DKIM key, say):")
			fmt.Println("  inbuxa dns --refresh --key -")
			return 0
		}
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "reading "+zoneFile+": "+err.Error())
			return 1
		}
		fmt.Printf("No saved records at %s. Reading them live from the server instead.\n\n", zoneFile)
	}

	if *key == "-" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "reading the key from standard input: "+err.Error())
			return 2
		}
		*key = strings.TrimSpace(line)
	}
	if *key == "" {
		fmt.Println("Reading the records live needs an API key: make one in the console (your account, API")
		fmt.Println("keys), then")
		fmt.Println("  inbuxa dns --refresh --key -")
		fmt.Println("and paste it, or set INBUXA_API_KEY. It isn't kept.")
		return 1
	}
	zone, err := apply.FetchDNSZone(ctx, st, dom, *key)
	if err != nil {
		if errors.Is(err, apply.ErrKeyRefused) {
			fmt.Fprintln(os.Stderr, "the server didn't take that API key")
			return 1
		}
		fmt.Fprintln(os.Stderr, "stopped: "+err.Error())
		return 1
	}
	fmt.Printf("The records %s needs published, read live from the server:\n\n", dom)
	fmt.Print(zone)
	if len(zone) > 0 && zone[len(zone)-1] != '\n' {
		fmt.Println()
	}
	// Keep the saved copy in step with what the server now says.
	if err := os.WriteFile(zoneFile, []byte(zone), 0o644); err == nil {
		fmt.Printf("\n(also written to %s)\n", zoneFile)
	}
	return 0
}

// relayCmd is inbuxa Relay on this machine, which shares nothing with the
// suite's install but the way it is asked for.
func relayCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "inbuxa relay install|status|uninstall\n\n"+usage)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "install":
		return relayInstall(ctx, args[1:])
	case "status":
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "the relay's status needs root: its state is its user's alone")
			return 1
		}
		if !relay.Status(ctx, os.Stdout) {
			return 1
		}
		return 0
	case "uninstall":
		return relayUninstall(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown relay command %q: install, status or uninstall\n", args[0])
	return 2
}

func clusterCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "inbuxa cluster doctor|join [flags]\n\n"+usage)
		return 2
	}
	switch args[0] {
	case "doctor":
		return clusterDoctor(args[1:])
	case "join":
		return clusterJoin(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown cluster command %q: doctor, join\n", args[0])
	return 2
}

// clusterJoin points this node's server at the shared store named in the
// topology file's cluster block for this machine, places the secrets every
// node reads from disk, gives the node its role and boot order, and restarts
// it. It never reaches another machine: the file is copied across and run on
// each node, as the rest of the installer works. The registry that names the
// blob store and coordinator is set up once for the cluster, not here.
func clusterJoin(args []string) int {
	fs := flag.NewFlagSet("cluster join", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		file        = fs.String("f", "", "")
		machineName = fs.String("machine", "", "")
		dryRun      = fs.Bool("dry-run", false, "")
		yes         = fs.Bool("yes", false, "")
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
		name, _ = os.Hostname()
	}
	m, ok := t.Machine(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "this machine is %q, which the file does not mention.\n", name)
		fmt.Fprintf(os.Stderr, "It describes: %s\n", strings.Join(machineNames(t), ", "))
		fmt.Fprintln(os.Stderr, "Pass --machine to say which one this is.")
		return 1
	}
	if !m.Clustered() {
		fmt.Fprintf(os.Stderr, "machine %q has no cluster block in this file; there is nothing to join.\n", name)
		return 1
	}
	if m.Shape("server") != "host" {
		fmt.Fprintf(os.Stderr, "a cluster node runs the mail server on the host; machine %q runs it as %q.\n", name, m.Shape("server"))
		return 1
	}

	etc := filepath.Join(m.Dir, "server")
	cfg := cluster.Config{
		Node:    *m.Cluster,
		Machine: name,
		EtcDir:  etc,
		Unit:    apply.HostServerUnit,
		UnitDir: filepath.Dir(discover.HostServerUnitPath),
		User:    apply.HostServerUser,
		DryRun:  *dryRun,
		Log:     func(format string, a ...any) { fmt.Printf("  "+format+"\n", a...) },
	}
	p, err := cluster.BuildPlan(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Print(p.String())

	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out, or --dry-run to see the files.")
		return 1
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "\nthis writes under the server's directory and /etc/systemd: run it as root")
		return 1
	}

	fmt.Println("\nJoining:")
	if err := cluster.Apply(context.Background(), p); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	if st, err := state.Load(); err == nil {
		st.Clustered, st.ClusterRole = true, m.Cluster.Role
		if err := st.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "could not record cluster membership in %s: %v\n", state.Path, err)
		}
	}
	fmt.Printf("\nDone. %s is in the cluster as %s.\n", cfg.Machine, roleWordCLI(m.Cluster.Role))
	return 0
}

func roleWordCLI(role string) string {
	if role == "" {
		return "the node that carries every task (no role)"
	}
	return "role " + role
}

// clusterDoctor probes the data-layer endpoints the operator names, so a node
// can be checked before it is pointed at a cluster. It touches nothing.
func clusterDoctor(args []string) int {
	fs := flag.NewFlagSet("cluster doctor", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		pg      = fs.String("pg", "", "")
		nats    = fs.String("nats", "", "")
		etcd    = fs.String("etcd", "", "")
		patroni = fs.String("patroni", "", "")
		s3      = fs.String("s3", "", "")
		timeout = fs.Duration("timeout", 5*time.Second, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected %q: cluster doctor takes only flags\n", fs.Arg(0))
		return 2
	}

	var targets []cluster.Target
	add := func(addr string, t cluster.Target) {
		if addr != "" {
			targets = append(targets, t)
		}
	}
	add(*pg, cluster.Target{Name: "PostgreSQL", Kind: cluster.TCP, Addr: *pg})
	add(*nats, cluster.Target{Name: "NATS", Kind: cluster.NATS, Addr: *nats})
	add(*etcd, cluster.Target{Name: "etcd", Kind: cluster.HTTP, Addr: "http://" + *etcd + "/health"})
	add(*patroni, cluster.Target{Name: "Patroni leader", Kind: cluster.HTTP, Addr: "http://" + *patroni + "/primary", HealthyStatus: 200})
	add(*s3, cluster.Target{Name: "S3 blobs", Kind: cluster.HTTP, Addr: "http://" + *s3 + "/health"})

	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to check: name an endpoint, such as --pg HOST:PORT (inbuxa cluster doctor -h)")
		return 2
	}

	checks := cluster.Doctor(context.Background(), targets, *timeout)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range checks {
		mark := "ok"
		if !c.OK {
			mark = "unreachable"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.Addr, mark, c.Detail)
	}
	w.Flush()
	if !cluster.AllOK(checks) {
		fmt.Fprintln(os.Stderr, "\nsome endpoints didn't answer; a node can't join until they do")
		return 1
	}
	return 0
}

func relayInstall(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("relay install", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		o      relay.Options
		token  = fs.String("token", os.Getenv("INBUXA_RELAY_JOIN_TOKEN"), "")
		dryRun = fs.Bool("dry-run", false, "")
		yes    = fs.Bool("yes", false, "")
	)
	fs.StringVar(&o.LinkListen, "link-listen", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected %q: the token goes after --token, or \"--token -\" reads it from standard input\n", fs.Arg(0))
		return 2
	}
	// Read before the plan, which needs to know whether there is one; a
	// plan that then refuses has still read it, and forgets it.
	t, err := readKey(*token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	o.Token = t
	p, err := relay.Check(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot install the relay: "+err.Error())
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
	fmt.Println("\nInstalling:")
	if err := relay.Install(ctx, p, o, &printer{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println()
	relay.Status(ctx, os.Stdout)
	fmt.Println("\nDone. Within a few seconds its card in the console says it is linked, and it opens")
	fmt.Println("port 25 once the server has sent it its settings. `inbuxa relay status` shows it here.")
	return 0
}

func relayUninstall(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("relay uninstall", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	var (
		purge       = fs.Bool("purge", false, "")
		discardHeld = fs.Bool("discard-held", false, "")
		dryRun      = fs.Bool("dry-run", false, "")
		yes         = fs.Bool("yes", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *discardHeld && !*purge {
		fmt.Fprintln(os.Stderr, "--discard-held goes with --purge: without it, the held mail is kept")
		return 2
	}
	if !relay.Installed() {
		fmt.Println("inbuxa Relay isn't installed here.")
		return 0
	}
	remove, keep := relay.UninstallPlan(*purge)
	fmt.Println("Taken away:")
	for _, x := range remove {
		fmt.Println("  - " + x)
	}
	if len(keep) > 0 {
		fmt.Println("\nKept:")
		for _, x := range keep {
			fmt.Println("  - " + x)
		}
	}
	if *purge {
		fmt.Println("\nWorth knowing:")
		fmt.Println("  - Remove it in the console too (the bin on its card), or the server goes on trying to link to it.")
	}
	if *dryRun {
		return 0
	}
	if !*yes {
		fmt.Fprintln(os.Stderr, "\nNothing has happened yet. Pass --yes to carry this out.")
		return 1
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uninstalling needs root")
		return 1
	}
	fmt.Println("\nUninstalling:")
	if err := relay.Uninstall(ctx, *purge, *discardHeld, &printer{}); err != nil {
		fmt.Fprintln(os.Stderr, "\nstopped: "+err.Error())
		return 1
	}
	fmt.Println("\nDone.")
	return 0
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func shapeComponent(s string) (string, error) {
	switch strings.ToLower(s) {
	case "server", "console", "webmail":
		return strings.ToLower(s), nil
	}
	return "", fmt.Errorf("%q is not a component: server, console or webmail", s)
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
	if st.Removed() {
		fmt.Printf("Nothing is installed on this machine: uninstalled %s, with its data kept in %s.\n",
			st.RemovedAt.Format("2006-01-02 15:04 UTC"), st.Dir)
		fmt.Println("`inbuxa install` with the same options brings it back; `inbuxa uninstall --purge` removes the data.")
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
		case (kind == "server" && found.HostServer) || (kind == "console" && found.HostConsole) || (kind == "webmail" && found.HostWebmail):
			fmt.Printf("  %-8s %-24s %s\n", kind, "on the host", state)
		default:
			fmt.Printf("  %-8s %-24s %s\n", kind, "in the deployment", state)
		}
	}

	if steps := apply.UpgradePlan(st); len(steps) > 0 {
		fmt.Println("\nVersions")
		for _, s := range steps {
			line := s.From
			if s.Due() {
				line += "   (this installer installs " + s.To + ": inbuxa upgrade)"
			}
			fmt.Printf("  %-8s %s\n", s.Component, line)
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

// doctorCmd is a one-shot, read-only health check of the install on this
// machine: the services that should run, the certificate the mail ports
// present, those ports answering, and the DNS a sender needs. It changes
// nothing, and exits 1 only when something has failed -- a warning, such as
// DNS that has not spread yet, does not fail it.
func doctorCmd(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	timeout := fs.Duration("timeout", 5*time.Second, "")
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
	found := discover.Run(ctx, f, st.Dir)
	if !st.Installed() && found.Dir == "" {
		fmt.Println("Nothing is installed on this machine; there is nothing to check.")
		return 0
	}
	if st.Removed() {
		fmt.Println("The programs were uninstalled here and the data kept; there is nothing running to check.")
		return 0
	}

	var r doctor.Report
	for _, c := range doctor.ServiceChecks(st.Shapes, found.Services) {
		r.Add(c)
	}

	_, serverHere := st.Shapes["server"]
	mailHost := apply.CertsMailHost("", st, found.MailHost)
	local := apply.LocalInstall(st)

	if serverHere {
		// The certificate the mail ports present, unless this is --local.
		var problem string
		var perr error
		if !local {
			s, err := apply.ReadServed(ctx, mailHost, apply.CertRoots(st.Dir))
			if err != nil {
				perr = err
			} else {
				problem = s.Problem(mailHost, time.Now())
			}
		}
		r.Add(doctor.CertCheck(local, perr, problem))

		// The ports the server should answer on, dialed on loopback.
		ports := []struct {
			n    int
			for_ string
		}{
			{25, "SMTP, mail from other servers"},
			{465, "submissions, apps sending"},
			{993, "IMAPS, apps reading"},
			{443, "HTTPS, the front ends and JMAP"},
		}
		var probes []doctor.PortProbe
		for _, p := range ports {
			pr := doctor.PortProbe{Port: p.n, For: p.for_}
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.n), *timeout)
			if err == nil {
				pr.Open = true
				conn.Close()
			}
			probes = append(probes, pr)
		}
		for _, c := range doctor.PortChecks(probes) {
			r.Add(c)
		}
	}

	// The DNS a sending server needs, for a public install that runs the
	// server here.
	df := doctor.DNSFacts{MailHost: mailHost}
	domain := st.Domain
	if domain == "" {
		domain = found.Domain
	}
	if serverHere && !local && domain != "" {
		df.Domain = domain
		if mxs, err := net.DefaultResolver.LookupMX(ctx, domain); err != nil {
			df.MXErr = resolverReason(err)
		} else {
			for _, mx := range mxs {
				df.MX = append(df.MX, strings.TrimSuffix(mx.Host, "."))
			}
		}
		if addrs, err := net.DefaultResolver.LookupHost(ctx, mailHost); err != nil {
			df.HostErr = resolverReason(err)
		} else {
			df.HostAddrs = addrs
		}
	}
	for _, c := range doctor.DNSChecks(df) {
		r.Add(c)
	}

	printDoctorReport(r)
	return r.ExitCode()
}

// resolverReason trims a resolver error to its reason, dropping the "lookup
// name on server:" preamble a *net.DNSError carries.
func resolverReason(err error) string {
	var d *net.DNSError
	if errors.As(err, &d) {
		return d.Err
	}
	return err.Error()
}

// printDoctorReport prints each check, then one line on the whole.
func printDoctorReport(r doctor.Report) {
	mark := map[doctor.Level]string{
		doctor.OK: "ok  ", doctor.Warn: "warn", doctor.Fail: "FAIL", doctor.Skipped: "--  ",
	}
	for _, c := range r.Checks {
		fmt.Printf("  [%s] %-16s %s\n", mark[c.Level], c.Name, c.Detail)
	}
	switch r.Worst() {
	case doctor.Fail:
		fmt.Println("\nSomething above is wrong. `inbuxa status` shows what is installed, `inbuxa certs`")
		fmt.Println("looks after the certificate, and `inbuxa dns` prints the records the domain needs.")
	case doctor.Warn:
		fmt.Println("\nUp and running. The warnings above are worth a look -- DNS often just needs time.")
	default:
		fmt.Println("\nEverything checks out.")
	}
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
// allows. The interface's first screen is the same text.
func render(f host.Facts) string { return plan.SurveyText(f) }

// readKey is an API key given as --key: "-" reads it from standard input,
// so it stays out of the shell's history.
func readKey(key string) (string, error) {
	if key != "-" {
		return key, nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading the key from standard input: %w", err)
	}
	return strings.TrimSpace(line), nil
}
