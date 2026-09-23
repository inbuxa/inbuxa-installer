// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plan turns what the operator chose, and what the machine is, into
// the list of things that would happen -- before any of them do.
//
// Everything the installer does goes through a plan. The interface shows it
// on its fifth screen, `--dry-run` prints it and stops, and apply walks it.
// One list, three readers: what you are shown is what runs.
package plan

import (
	"fmt"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

// Component is one of the three programs.
type Component string

const (
	Server  Component = "server"
	Console Component = "console"
	Webmail Component = "webmail"
)

// Shape is how a component is installed here.
type Shape string

const (
	Skip      Shape = "skip"
	Container Shape = "container"
	Host      Shape = "host"
)

// Names as they are shown to a person. The programs have names; the
// components have jobs.
var names = map[Component]string{
	Server:  "inbuxa-server (the mail server)",
	Console: "inbuxa Admin (the console)",
	Webmail: "inbuxa webmail",
}

// Choice is one row of the matrix.
type Choice struct {
	Component Component
	Shape     Shape
}

// Options is everything the six screens collect.
type Options struct {
	Domain      string
	MailHost    string
	ConsoleHost string
	WebmailHost string
	ACMEEmail   string
	Local       bool // loopback evaluation: no public ports, no certificates
	Dir         string
	Proxy       string // "caddy", "snippets", "none"
	Choices     []Choice
	InstallDeps bool // resolve what is missing rather than refusing over it

	// A private ACME CA, for testing the certificate path without asking a
	// public CA for certificates for names that are not ours. Empty means
	// Let's Encrypt, which is what an install should use.
	ACMEDirectory string
	ACMECARoot    string
}

// Shape returns what was chosen for a component.
func (o Options) Shape(c Component) Shape {
	for _, ch := range o.Choices {
		if ch.Component == c {
			return ch.Shape
		}
	}
	return Skip
}

// Defaults fills in the names that follow from the domain, leaving anything
// the operator set alone.
func (o *Options) Defaults() {
	if o.Domain == "" {
		return
	}
	if o.MailHost == "" {
		o.MailHost = "mail." + o.Domain
	}
	if o.ConsoleHost == "" {
		o.ConsoleHost = "admin." + o.Domain
	}
	if o.WebmailHost == "" {
		o.WebmailHost = "webmail." + o.Domain
	}
	if o.ACMEEmail == "" {
		o.ACMEEmail = "postmaster@" + o.Domain
	}
	if o.Dir == "" {
		o.Dir = "/var/lib/inbuxa"
	}
	if o.Proxy == "" {
		o.Proxy = "caddy"
	}
}

// Availability says whether a shape can be chosen for a component on this
// machine, and why not when it cannot. The interface dims what it cannot
// offer and shows the reason beside it; a flag that asks for it anyway gets
// the same sentence as an error.
type Availability struct {
	OK  bool
	Why string
}

// Available answers for one cell of the matrix.
func Available(f host.Facts, c Component, s Shape) Availability {
	switch s {
	case Skip:
		return Availability{OK: true}
	case Container:
		if !f.Docker.Present {
			return Availability{Why: "docker is not installed"}
		}
		if !f.Docker.Usable {
			return Availability{Why: f.Docker.Why}
		}
		return Availability{OK: true}
	case Host:
		if !f.Systemd {
			return Availability{Why: "a host install needs systemd, which is not running this machine"}
		}
		switch c {
		case Server:
			return Availability{OK: true}
		case Console:
			return Availability{OK: true}
		case Webmail:
			if !f.Node.Present {
				return Availability{Why: "a host install of the webmail needs Node 22 or newer; none is installed"}
			}
			if f.Node.Major < 22 {
				return Availability{Why: fmt.Sprintf("a host install of the webmail needs Node 22 or newer; found %s", f.Node.Version)}
			}
			return Availability{OK: true}
		}
	}
	return Availability{Why: "unknown shape"}
}

// Step is one thing the installer will do, in the order it will do it.
type Step struct {
	Title  string   // shown as a line in the plan and ticked off during apply
	Detail []string // the files, units, containers and ports it touches
}

// Plan is the whole of it.
type Plan struct {
	Options  Options
	Steps    []Step
	Ports    []int       // what will be bound, once, across every component
	DNS      []string    // records the domain needs for this shape
	Warnings []string    // things that are not refusals but should be read
	Needs    []deps.Need // what is missing, and what would be done about it
}

// Build works out what would happen. It does not touch the machine: every
// fact it needs is in the survey it was given.
func Build(f host.Facts, o Options) (Plan, error) {
	o.Defaults()
	p := Plan{Options: o}

	if o.Domain == "" && !o.Local {
		return p, fmt.Errorf("a domain is needed (or --local for a loopback evaluation)")
	}
	chosen, wantContainers, wantHostWebmail := 0, false, false
	for _, c := range []Component{Server, Console, Webmail} {
		switch o.Shape(c) {
		case Skip:
		case Container:
			chosen++
			wantContainers = true
		case Host:
			chosen++
			if c == Webmail {
				wantHostWebmail = true
			}
		}
	}
	if chosen == 0 {
		return p, fmt.Errorf("nothing chosen: pick at least one component")
	}

	// What is missing is not the same as what is impossible. Docker absent on
	// a Debian machine is one package and a service; Node too old is a pinned
	// tarball. The installer says what it would do about each and does it when
	// told, rather than handing the operator a chore and calling it an error.
	p.Needs = deps.For(f, wantContainers, wantHostWebmail)

	for _, c := range []Component{Server, Console, Webmail} {
		s := o.Shape(c)
		if s == Skip {
			continue
		}
		a := Available(f, c, s)
		if a.OK {
			continue
		}
		if covered(p.Needs, c, s) && o.InstallDeps {
			continue
		}
		if covered(p.Needs, c, s) {
			return p, fmt.Errorf("%s as a %s install: %s\n\nThe installer can fix that:\n%s\nPass --install-deps to let it, or choose another shape",
				names[c], s, a.Why, deps.Describe(p.Needs))
		}
		return p, fmt.Errorf("%s as a %s install: %s", names[c], s, a.Why)
	}

	if len(p.Needs) > 0 && o.InstallDeps {
		var detail []string
		for _, n := range p.Needs {
			for _, a := range n.Actions {
				detail = append(detail, n.Name+": "+a)
			}
		}
		p.Steps = append(p.Steps, Step{Title: "Install what this machine is missing", Detail: detail})
	}

	if !f.Root {
		p.Warnings = append(p.Warnings, "not running as root: installing units, users and files under /etc will fail")
	}
	if f.MemoryMB > 0 && f.MemoryMB < 2048 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d MB of memory: a mail server with a full text index wants 2 GB or more", f.MemoryMB))
	}
	if f.DiskFreeGB > 0 && f.DiskFreeGB < 10 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d GB free: mail, indexes and images need room to grow", f.DiskFreeGB))
	}
	if f.Existing != "" {
		p.Warnings = append(p.Warnings, "an install is already recorded in "+f.Existing+": this run will converge it, not duplicate it")
	}

	p.Steps = append(p.Steps, Step{
		Title:  "Write the deployment directory",
		Detail: []string{o.Dir + "/ (configuration, state and the credentials file)"},
	})

	if s := o.Shape(Server); s != Skip {
		p.Ports = append(p.Ports, 25, 465, 993, 995, 4190)
		switch s {
		case Container:
			p.Steps = append(p.Steps, Step{
				Title: "Start the mail server as a container",
				Detail: []string{
					"image: registry.coffeylabs.org/inbuxa/inbuxa-server (pinned by digest)",
					"volumes: inbuxa-data, inbuxa-etc",
					"ports: 25, 465, 993, 995, 4190, and 8081 on loopback for the proxy",
				},
			})
		case Host:
			p.Steps = append(p.Steps, Step{
				Title: "Install the mail server on this machine",
				Detail: []string{
					"binary: /usr/local/bin/inbuxa-server, from the release on git.coffeylabs.org",
					"user: inbuxa (system, no login), data in " + o.Dir + "/server",
					"unit: /etc/systemd/system/inbuxa-server.service",
				},
			})
		}
		p.Steps = append(p.Steps, Step{
			Title: "First boot",
			Detail: []string{
				"bootstrap over JMAP, then set the front-end URLs, which registers both first-party OAuth clients",
				"turn on ACME, trust the proxy, create the first account",
				"write " + o.Dir + "/credentials and " + o.Dir + "/dns.zone",
			},
		})
	}

	if s := o.Shape(Console); s != Skip {
		switch s {
		case Container:
			p.Steps = append(p.Steps, Step{
				Title: "Start the console as a container",
				Detail: []string{
					"image: registry.coffeylabs.org/inbuxa/inbuxa-admin (pinned by digest)",
					"port: 8082 on loopback, behind the proxy",
				},
			})
		case Host:
			p.Steps = append(p.Steps, Step{
				Title: "Install the console on this machine",
				Detail: []string{
					"files: " + o.Dir + "/console (static, served by the proxy)",
					"no service: it is a page, not a program",
				},
			})
		}
	}

	if s := o.Shape(Webmail); s != Skip {
		switch s {
		case Container:
			p.Steps = append(p.Steps, Step{
				Title: "Start the webmail as a container",
				Detail: []string{
					"image: registry.coffeylabs.org/inbuxa/ihasmail-inbuxa (pinned by digest)",
					"port: 8080 on loopback, behind the proxy",
					"secret: the OAuth client secret from first boot, written once",
				},
			})
		case Host:
			p.Steps = append(p.Steps, Step{
				Title: "Install the webmail on this machine",
				Detail: []string{
					"files: " + o.Dir + "/webmail, from the release tarball",
					"user: inbuxa-webmail (system, no login)",
					"unit: /etc/systemd/system/inbuxa-webmail.service, listening on 127.0.0.1:8080",
				},
			})
		}
	}

	if !o.Local {
		switch o.Proxy {
		case "caddy":
			p.Ports = append(p.Ports, 80, 443)
			p.Steps = append(p.Steps, Step{
				Title: "Put a proxy in front, with certificates",
				Detail: []string{
					"caddy, managed by the installer",
					"names: " + strings.Join(o.hostnames(), ", "),
					"ports: 80 and 443",
				},
			})
		case "snippets":
			p.Steps = append(p.Steps, Step{
				Title: "Write proxy configuration for the web server already here",
				Detail: []string{
					o.Dir + "/proxy/nginx-inbuxa.conf and " + o.Dir + "/proxy/Caddyfile.fragment",
					"nothing is loaded or restarted: that stays yours",
				},
			})
		}
	}

	p.Steps = append(p.Steps, Step{
		Title: "Check it works",
		Detail: []string{
			"the server answers JMAP and reports its version",
			"each front end answers, and points at the server it was configured with",
			"the ports that should be open are, and the ones that should not are not",
		},
	})

	p.DNS = o.dnsRecords()

	for _, held := range f.PortsHeld(p.Ports...) {
		who := held.Holder
		if who == "" {
			who = "something this user cannot see"
		}
		p.Warnings = append(p.Warnings, fmt.Sprintf("port %d (%s) is held by %s", held.Number, held.For, who))
	}
	return p, nil
}

// covered says whether a cell's unavailability is one of the things the
// installer offered to fix.
func covered(needs []deps.Need, c Component, s Shape) bool {
	want := map[string]bool{}
	switch {
	case s == Container:
		want["docker"], want["compose"] = true, true
	case s == Host && c == Webmail:
		want["node"] = true
	default:
		return false
	}
	found := false
	for _, n := range needs {
		if !want[n.Name] {
			continue
		}
		if !n.Fixable {
			return false
		}
		found = true
	}
	return found
}

func (o Options) hostnames() []string {
	var names []string
	if o.Shape(Server) != Skip {
		names = append(names, o.MailHost)
	}
	if o.Shape(Console) != Skip {
		names = append(names, o.ConsoleHost)
	}
	if o.Shape(Webmail) != Skip {
		names = append(names, o.WebmailHost)
	}
	return names
}

func (o Options) dnsRecords() []string {
	if o.Local || o.Domain == "" {
		return nil
	}
	var r []string
	if o.Shape(Server) != Skip {
		r = append(r,
			fmt.Sprintf("%-28s MX    10 %s.", o.Domain+".", o.MailHost),
			fmt.Sprintf("%-28s A     this machine", o.MailHost+"."),
			fmt.Sprintf("%-28s TXT   \"v=spf1 mx -all\"", o.Domain+"."),
			fmt.Sprintf("%-28s TXT   the DKIM key first boot generates", "*._domainkey."+o.Domain+"."),
			fmt.Sprintf("%-28s TXT   \"v=DMARC1; p=reject; rua=mailto:postmaster@%s\"", "_dmarc."+o.Domain+".", o.Domain),
		)
	}
	if o.Shape(Console) != Skip {
		r = append(r, fmt.Sprintf("%-28s A     this machine", o.ConsoleHost+"."))
	}
	if o.Shape(Webmail) != Skip {
		r = append(r, fmt.Sprintf("%-28s A     this machine", o.WebmailHost+"."))
	}
	r = append(r, "(first boot writes the exact records, including the keys, to dns.zone)")
	return r
}

// String renders the plan the way the fifth screen and --dry-run show it.
func (p Plan) String() string {
	var b strings.Builder
	o := p.Options
	fmt.Fprintf(&b, "Plan for %s\n\n", describe(o))
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "%2d. %s\n", i+1, s.Title)
		for _, d := range s.Detail {
			fmt.Fprintf(&b, "      %s\n", d)
		}
	}
	if len(p.Ports) > 0 {
		fmt.Fprintf(&b, "\nPorts it will bind: %s\n", joinInts(p.Ports))
	}
	if len(p.DNS) > 0 {
		b.WriteString("\nThe domain will need:\n")
		for _, r := range p.DNS {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	if len(p.Warnings) > 0 {
		b.WriteString("\nWorth reading first:\n")
		for _, w := range p.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}
	return b.String()
}

func describe(o Options) string {
	var parts []string
	for _, c := range []Component{Server, Console, Webmail} {
		if s := o.Shape(c); s != Skip {
			parts = append(parts, fmt.Sprintf("%s as a %s install", names[c], s))
		}
	}
	where := o.Domain
	if o.Local {
		where = "a loopback evaluation"
	}
	return strings.Join(parts, ", ") + " -- " + where
}

func joinInts(n []int) string {
	seen := map[int]bool{}
	var out []string
	for _, i := range n {
		if seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, fmt.Sprint(i))
	}
	return strings.Join(out, ", ")
}
