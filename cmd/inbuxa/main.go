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
	"flag"
	"fmt"
	"os"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

// version is stamped by the release build; a build from a working tree says
// so rather than claiming a release it is not.
var version = "dev"

const usage = `inbuxa -- install the inbuxa suite on this machine

  inbuxa install [flags]     install or converge (no flags: the interface)
  inbuxa survey              what this machine is, as the installer sees it
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
  --dry-run                  print the plan and stop
  --yes                      do not ask for confirmation
`

func main() {
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
		fmt.Fprintln(os.Stderr, "\nnothing has happened yet: applying is not built in this build (pass --dry-run to silence this)")
		return 1
	}
	fmt.Fprintln(os.Stderr, "\napply is not built yet")
	return 1
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

	docker := "not installed"
	switch {
	case f.Docker.Usable:
		docker = "usable, server " + f.Docker.Version + ", compose " + f.Docker.Compose
	case f.Docker.Present:
		docker = "installed but not usable: " + f.Docker.Why
	}
	fmt.Fprintf(&b, "  %-22s %s\n", "docker", docker)

	node := "not installed"
	switch {
	case f.Node.Present && f.Node.Major >= 22:
		node = f.Node.Version
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
