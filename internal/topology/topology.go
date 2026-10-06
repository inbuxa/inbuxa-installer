// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package topology is the description of a whole installation: which machine
// runs what, under which names, reachable from where.
//
// One file, however many machines. Each machine reads the same file and acts
// on its own part of it -- so the file is the thing an operator keeps, diffs
// and reviews, and no machine ever reaches another. That is the whole design:
// the designer, whatever draws it, emits this; `inbuxa apply -f` on each
// machine is the only thing with privileges.
//
// It describes front ends scaled out across machines, not a clustered mail
// server: one server, as many webmails and consoles as there are machines to
// put them on. A second mail node needs a shared store and cluster
// configuration, which is a larger product; the file's shape leaves room for
// it -- components on machines, with relationships -- so that version adds a
// kind rather than inventing a new file.
package topology

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Topology is the file.
type Topology struct {
	Version     int       `json:"version"`
	Domain      string    `json:"domain"`
	MailHost    string    `json:"mail_host,omitempty"`
	ConsoleHost string    `json:"console_host,omitempty"`
	WebmailHost string    `json:"webmail_host,omitempty"`
	ACMEEmail   string    `json:"acme_email,omitempty"`
	Machines    []Machine `json:"machines"`
}

// Machine is one host, named by the operator. The name is how a machine
// recognises its own part of the file; it defaults to the hostname, which is
// what most people will call it anyway.
type Machine struct {
	Name       string      `json:"name"`
	Components []Component `json:"components"`
	Proxy      string      `json:"proxy,omitempty"` // caddy, snippets, none
	Dir        string      `json:"dir,omitempty"`
}

// Component is one program on one machine.
type Component struct {
	Kind  string `json:"kind"`  // server, console, webmail
	Shape string `json:"shape"` // container, host
}

const Version = 1

var kinds = map[string]bool{"server": true, "console": true, "webmail": true}
var shapes = map[string]bool{"container": true, "host": true}

// Load reads a topology file and checks that it describes something that
// could exist. A file that cannot be installed should fail here, where the
// message can name the line's subject, rather than half way through an apply.
func Load(path string) (*Topology, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Topology
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &t, nil
}

// Validate is the rules. They are few on purpose: this describes front ends
// spread over machines, and the interesting refusals are about arrangements
// that cannot work rather than about syntax.
func (t *Topology) Validate() error {
	if t.Version != Version {
		return fmt.Errorf("version %d: this installer writes and reads version %d", t.Version, Version)
	}
	if t.Domain == "" {
		return fmt.Errorf("no domain")
	}
	t.Defaults()

	if len(t.Machines) == 0 {
		return fmt.Errorf("no machines")
	}
	seen := map[string]bool{}
	servers := 0
	for _, m := range t.Machines {
		if m.Name == "" {
			return fmt.Errorf("a machine has no name")
		}
		if seen[m.Name] {
			return fmt.Errorf("two machines are called %q", m.Name)
		}
		seen[m.Name] = true
		if len(m.Components) == 0 {
			return fmt.Errorf("machine %q runs nothing", m.Name)
		}
		kindsHere := map[string]bool{}
		for _, c := range m.Components {
			if !kinds[c.Kind] {
				return fmt.Errorf("machine %q: %q is not a component (server, console, webmail)", m.Name, c.Kind)
			}
			if !shapes[c.Shape] {
				return fmt.Errorf("machine %q: %q is not a shape (container, host)", m.Name, c.Shape)
			}
			if kindsHere[c.Kind] {
				// Two webmails on one machine would need two ports and two
				// names, and nothing here says which. Two machines is the
				// supported way to have two.
				return fmt.Errorf("machine %q runs two %ss; put the second on another machine", m.Name, c.Kind)
			}
			kindsHere[c.Kind] = true
			if c.Kind == "server" {
				servers++
			}
		}
	}
	switch servers {
	case 1:
	case 0:
		return fmt.Errorf("no machine runs the mail server")
	default:
		return fmt.Errorf("%d machines run the mail server: one mail server for now, because a second "+
			"node needs a shared store and cluster configuration, which this installer does not set up", servers)
	}
	return nil
}

// Defaults fills in the names that follow from the domain.
func (t *Topology) Defaults() {
	if t.MailHost == "" {
		t.MailHost = "mail." + t.Domain
	}
	if t.ConsoleHost == "" {
		t.ConsoleHost = "admin." + t.Domain
	}
	if t.WebmailHost == "" {
		t.WebmailHost = "webmail." + t.Domain
	}
	if t.ACMEEmail == "" {
		t.ACMEEmail = "postmaster@" + t.Domain
	}
	for i := range t.Machines {
		if t.Machines[i].Dir == "" {
			t.Machines[i].Dir = "/var/lib/inbuxa"
		}
		if t.Machines[i].Proxy == "" {
			t.Machines[i].Proxy = "caddy"
		}
	}
}

// Machine finds one by name.
func (t *Topology) Machine(name string) (Machine, bool) {
	for _, m := range t.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return Machine{}, false
}

// Others is every machine but this one, for the part of a plan that says what
// has to be run elsewhere. Nothing here reaches them: it prints the command
// and leaves it to the operator, which is the point.
func (t *Topology) Others(name string) []Machine {
	var out []Machine
	for _, m := range t.Machines {
		if m.Name != name {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Shape is what this machine runs a component as, or "" for not at all.
func (m Machine) Shape(kind string) string {
	for _, c := range m.Components {
		if c.Kind == kind {
			return c.Shape
		}
	}
	return ""
}

// Save writes a topology file, formatted to be read and diffed by people.
func (t *Topology) Save(path string) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
