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
// Version 1 described front ends scaled out across machines, not a clustered
// mail server: one server, as many webmails and consoles as there are machines
// to put them on. Version 2 adds the second case without a new file: a machine
// that runs the server may carry a cluster block, which says where this node's
// shared store, coordinator and blob store are and what role it takes. The
// addresses in it are the operator's; nothing in the file is specific to any
// one cluster. A file with no cluster block is still a version 1 file in every
// way that matters, and this installer reads both.
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
	Name       string       `json:"name"`
	Components []Component  `json:"components"`
	Proxy      string       `json:"proxy,omitempty"` // caddy, snippets, none
	Dir        string       `json:"dir,omitempty"`
	Cluster    *ClusterNode `json:"cluster,omitempty"` // version 2: this node's place in a cluster
}

// Component is one program on one machine.
type Component struct {
	Kind  string `json:"kind"`  // server, console, webmail
	Shape string `json:"shape"` // container, host
}

// ClusterNode is a server machine's membership in a clustered install: the
// shared data store this node reads and writes, the coordinator and blob store
// every node reaches, the node's role, and what it must start after. Every
// address in it is the operator's; the installer bakes in none of its own.
//
// What a joining node needs on disk is a store it can reach and the secrets
// the shared registry's File references point at. The coordinator and blob
// store are named here so the file is complete and so their secrets are placed
// beside the store's; the registry that points the cluster at them is set up
// once, as its own step, not by every node that joins.
type ClusterNode struct {
	Store       ClusterStore    `json:"store"`                 // this node's shared data store (required)
	Coordinator *ClusterService `json:"coordinator,omitempty"` // the message coordinator every node reads
	BlobStore   *ClusterBlob    `json:"blob_store,omitempty"`  // the blob store every node reads
	Role        string          `json:"role,omitempty"`        // the node's cluster role; empty on the node that carries every task
	Hostname    string          `json:"hostname,omitempty"`    // what the node calls itself (INBUXA_HOSTNAME); the machine name by default
	After       []string        `json:"after,omitempty"`       // units this node must start after, so it reaches the store at boot
}

// ClusterStore is the shared store a node points its server at, in place of
// the single-node built-in store. It is a SQL store: the one kind more than
// one server can share.
type ClusterStore struct {
	Type       string `json:"type"`                  // the server store type, e.g. "PostgreSql"
	Host       string `json:"host"`                  // where this node reaches the store
	Port       int    `json:"port"`                  //
	Database   string `json:"database"`              //
	User       string `json:"user"`                  // the database user
	SecretFile string `json:"secret_file"`           // the file the server reads the password from
	SecretFrom string `json:"secret_from,omitempty"` // a source to install that file from, 0400; empty means it is already there
	TLS        bool   `json:"tls,omitempty"`         //
	PoolMax    int    `json:"pool_max,omitempty"`    // connections in the pool; a sensible default when zero
}

// ClusterService is the coordinator (the message bus nodes agree through): the
// address the registry names, and the secret each node reads from a local file.
type ClusterService struct {
	Addr       string `json:"addr"`                  // host:port
	User       string `json:"user,omitempty"`        //
	SecretFile string `json:"secret_file"`           // the file the server reads the secret from
	SecretFrom string `json:"secret_from,omitempty"` // a source to install it from, 0400; empty means already there
}

// ClusterBlob is the blob store (where message bodies live): the endpoint the
// registry names, and the secret each node reads from a local file.
type ClusterBlob struct {
	Endpoint   string `json:"endpoint"`              // the S3 endpoint, host:port or URL
	Bucket     string `json:"bucket,omitempty"`      //
	Region     string `json:"region,omitempty"`      //
	SecretFile string `json:"secret_file"`           // the file the server reads the secret key from
	SecretFrom string `json:"secret_from,omitempty"` // a source to install it from, 0400; empty means already there
}

// Clustered says whether this machine carries cluster configuration.
func (m Machine) Clustered() bool { return m.Cluster != nil }

const Version = 2

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
	if t.Version < 1 || t.Version > Version {
		return fmt.Errorf("version %d: this installer reads versions 1 to %d", t.Version, Version)
	}
	if t.Domain == "" {
		return fmt.Errorf("no domain")
	}
	t.Defaults()

	if len(t.Machines) == 0 {
		return fmt.Errorf("no machines")
	}
	seen := map[string]bool{}
	servers, clustered := 0, 0
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
		if m.Cluster != nil {
			if t.Version < 2 {
				return fmt.Errorf("machine %q has a cluster block, which needs version 2", m.Name)
			}
			if !kindsHere["server"] {
				return fmt.Errorf("machine %q has a cluster block but runs no mail server; only a server joins a cluster", m.Name)
			}
			if err := m.Cluster.validate(m.Name); err != nil {
				return err
			}
			clustered++
		}
	}
	switch {
	case servers == 0:
		return fmt.Errorf("no machine runs the mail server")
	case servers == 1:
		// One server, clustered or not, is always allowed.
	case clustered == servers:
		// Every mail node carries cluster configuration: a clustered install.
	default:
		return fmt.Errorf("%d machines run the mail server but %d carry a cluster block: "+
			"a clustered install needs one on every mail node, or run a single server", servers, clustered)
	}
	return nil
}

// validate checks a cluster node describes a store the installer can point a
// server at and secrets it can place. The addresses themselves are the
// operator's to get right; what it refuses is a block that cannot be acted on.
func (c *ClusterNode) validate(machine string) error {
	fail := func(f string, a ...any) error {
		return fmt.Errorf("machine %q cluster: %s", machine, fmt.Sprintf(f, a...))
	}
	s := c.Store
	switch {
	case s.Type == "":
		return fail("the store needs a type, e.g. \"PostgreSql\"")
	case s.Host == "":
		return fail("the store needs a host")
	case s.Port <= 0 || s.Port > 65535:
		return fail("the store port %d is not a port", s.Port)
	case s.Database == "":
		return fail("the store needs a database name")
	case s.User == "":
		return fail("the store needs a user")
	case s.SecretFile == "":
		return fail("the store needs a secret_file: where the server reads the password")
	}
	if c.Coordinator != nil {
		if c.Coordinator.Addr == "" {
			return fail("the coordinator needs an addr")
		}
		if c.Coordinator.SecretFile == "" {
			return fail("the coordinator needs a secret_file")
		}
	}
	if c.BlobStore != nil {
		if c.BlobStore.Endpoint == "" {
			return fail("the blob store needs an endpoint")
		}
		if c.BlobStore.SecretFile == "" {
			return fail("the blob store needs a secret_file")
		}
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
