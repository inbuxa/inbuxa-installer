// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package doctor reports the health of an install on this machine in one
// pass: the services that should be running, the certificate a mail client
// is given, the mail ports answering, and the DNS a sender needs to reach
// it. Every check only reads; nothing here changes the install.
//
// The checks are pure functions over facts the caller has already gathered,
// so the command does the probing -- surveying services, reading the served
// certificate, dialing the ports, resolving names -- and this decides what
// each result means. That keeps the judgement testable without a live
// install.
package doctor

import (
	"fmt"
	"sort"
	"strings"
)

// Level is how a check came out.
type Level int

const (
	// OK is healthy.
	OK Level = iota
	// Warn is worth a look but not broken: often something outside the
	// machine, like DNS that has not propagated yet.
	Warn
	// Fail is broken: the install is not serving mail as it should.
	Fail
	// Skipped is a check that does not apply here, such as certificates on a
	// --local install.
	Skipped
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	case Skipped:
		return "skip"
	}
	return "?"
}

// Check is one thing looked at and what was found.
type Check struct {
	Name   string
	Level  Level
	Detail string
}

// Report is every check from a run.
type Report struct {
	Checks []Check
}

// Add appends a check.
func (r *Report) Add(c Check) { r.Checks = append(r.Checks, c) }

// Worst is the most serious level in the report, OK when empty. Skipped does
// not count as worse than OK.
func (r Report) Worst() Level {
	worst := OK
	for _, c := range r.Checks {
		if c.Level == Skipped {
			continue
		}
		if c.Level > worst {
			worst = c.Level
		}
	}
	return worst
}

// ExitCode is 1 when anything failed, else 0. A warning is not a failure: it
// is a heads-up, and a script that gates on doctor should not stop for DNS
// that is still spreading.
func (r Report) ExitCode() int {
	if r.Worst() == Fail {
		return 1
	}
	return 0
}

// ServiceChecks is one check per component the install means to run (its
// shape is not "skip"/"none"/""), from the discovered service states
// (component -> running|stopped|absent). A running service is OK; anything
// else is a failure.
func ServiceChecks(shapes, services map[string]string) []Check {
	var kinds []string
	for kind, shape := range shapes {
		switch strings.ToLower(shape) {
		case "", "skip", "no", "none":
			continue
		}
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	checks := make([]Check, 0, len(kinds))
	for _, kind := range kinds {
		name := "service: " + kind
		switch services[kind] {
		case "running":
			checks = append(checks, Check{name, OK, "running"})
		case "stopped":
			checks = append(checks, Check{name, Fail, "installed but stopped"})
		default: // "absent" or missing
			checks = append(checks, Check{name, Fail, "not running and not found in the deployment"})
		}
	}
	return checks
}

// CertCheck judges the certificate a mail client is served. local skips it
// (a --local install has no public certificate). A probe error -- the mail
// ports not answering with TLS -- is a failure. Otherwise problem is what
// the certificate checker found wrong, "" when nothing is: a certificate
// that is merely close to expiry without renewing is a warning, and anything
// else wrong (self-signed, expired, wrong name, untrusted) is a failure.
func CertCheck(local bool, probeErr error, problem string) Check {
	const name = "certificate"
	switch {
	case local:
		return Check{name, Skipped, "a --local install serves no public certificate"}
	case probeErr != nil:
		return Check{name, Fail, probeErr.Error()}
	case problem == "":
		return Check{name, OK, "verifies and is current"}
	case strings.HasPrefix(problem, "it expires "):
		// The only soft case the checker reports: valid today, but past the
		// point it should have renewed.
		return Check{name, Warn, problem}
	default:
		return Check{name, Fail, problem}
	}
}

// PortProbe is the result of dialing one port the server should answer on.
type PortProbe struct {
	Port int
	For  string // what the port is for, shown in the detail
	Open bool
	Err  string // why it did not answer, when it did not
}

// PortChecks is one check per probed port: open is OK, closed is a failure.
func PortChecks(probes []PortProbe) []Check {
	checks := make([]Check, 0, len(probes))
	for _, p := range probes {
		name := fmt.Sprintf("port %d", p.Port)
		if p.Open {
			checks = append(checks, Check{name, OK, p.For})
			continue
		}
		detail := p.For + ": no answer"
		if p.Err != "" {
			detail = p.For + ": " + p.Err
		}
		checks = append(checks, Check{name, Fail, detail})
	}
	return checks
}

// DNSFacts are the resolver answers the DNS checks judge.
type DNSFacts struct {
	Domain    string   // the mail domain
	MailHost  string   // the server's name, where its MX should point
	MX        []string // the domain's MX hosts, trailing dot trimmed
	MXErr     string   // why the MX lookup failed, when it did
	HostAddrs []string // the mail host's A/AAAA addresses
	HostErr   string   // why the host lookup failed, when it did
}

// DNSChecks judges the DNS a sending server needs: the domain's MX, and that
// the mail host it names resolves to an address. DNS that is missing or not
// yet pointing at this host is a warning, not a failure: a fresh install is
// healthy before its records are published or have spread. With no domain
// (a --local install, or one whose domain was never recorded) the checks are
// skipped.
func DNSChecks(f DNSFacts) []Check {
	if f.Domain == "" {
		return []Check{{"dns", Skipped, "no mail domain to check"}}
	}
	var checks []Check

	mx := Check{Name: "dns: MX"}
	switch {
	case f.MXErr != "":
		mx.Level, mx.Detail = Warn, f.Domain+" has no MX record yet ("+f.MXErr+")"
	case len(f.MX) == 0:
		mx.Level, mx.Detail = Warn, f.Domain+" has no MX record yet"
	case f.MailHost != "" && !containsFold(f.MX, f.MailHost):
		mx.Level, mx.Detail = Warn, f.Domain+" MX is "+strings.Join(f.MX, ", ")+", not "+f.MailHost
	default:
		mx.Level, mx.Detail = OK, f.Domain+" -> "+strings.Join(f.MX, ", ")
	}
	checks = append(checks, mx)

	if f.MailHost != "" {
		host := Check{Name: "dns: mail host"}
		switch {
		case f.HostErr != "":
			host.Level, host.Detail = Warn, f.MailHost+" does not resolve yet ("+f.HostErr+")"
		case len(f.HostAddrs) == 0:
			host.Level, host.Detail = Warn, f.MailHost+" does not resolve yet"
		default:
			host.Level, host.Detail = OK, f.MailHost+" -> "+strings.Join(f.HostAddrs, ", ")
		}
		checks = append(checks, host)
	}
	return checks
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(strings.TrimSuffix(h, "."), strings.TrimSuffix(needle, ".")) {
			return true
		}
	}
	return false
}
