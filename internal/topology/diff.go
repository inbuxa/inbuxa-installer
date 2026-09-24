// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

package topology

import (
	"fmt"
	"sort"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Change is one difference between what a machine runs and what the topology
// says it should.
type Change struct {
	Component string
	Was       string // shape it is in now, "" for absent
	Wants     string // shape the file asks for, "" for removed
}

// What it is, in a word, for the line that prints it.
func (c Change) Verb() string {
	switch {
	case c.Was == "" && c.Wants != "":
		return "add"
	case c.Was != "" && c.Wants == "":
		return "remove"
	case c.Was != c.Wants:
		return "change"
	default:
		return "keep"
	}
}

// Diff compares what this machine has against what the file asks of it.
//
// The comparison is against recorded intent and not only against what is
// running: a container that is stopped is still installed, and a plan that
// offered to install it again would be lying about what it is doing.
type Diff struct {
	Machine string
	Changes []Change
}

// Compare works out the difference for one machine.
func Compare(s *state.State, m Machine) Diff {
	d := Diff{Machine: m.Name}
	seen := map[string]bool{}
	for _, kind := range []string{"server", "console", "webmail"} {
		was, wants := s.Shapes[kind], m.Shape(kind)
		seen[kind] = true
		if was == "" && wants == "" {
			continue
		}
		d.Changes = append(d.Changes, Change{Component: kind, Was: was, Wants: wants})
	}
	// Anything recorded that is not a component this version knows about:
	// report it rather than silently leaving it, because an older or newer
	// installer put it here and this one is about to rewrite the deployment.
	var extra []string
	for kind := range s.Shapes {
		if !seen[kind] {
			extra = append(extra, kind)
		}
	}
	sort.Strings(extra)
	for _, kind := range extra {
		d.Changes = append(d.Changes, Change{Component: kind, Was: s.Shapes[kind]})
	}
	return d
}

// Empty is whether anything would happen.
func (d Diff) Empty() bool {
	for _, c := range d.Changes {
		if c.Verb() != "keep" {
			return false
		}
	}
	return true
}

// Removals is what would be taken off this machine.
func (d Diff) Removals() []Change {
	var out []Change
	for _, c := range d.Changes {
		if c.Verb() == "remove" {
			out = append(out, c)
		}
	}
	return out
}

// String is the diff as the plan shows it: what is here, what is asked for,
// and nothing has happened yet.
func (d Diff) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "On %s\n", d.Machine)
	if d.Empty() {
		for _, c := range d.Changes {
			fmt.Fprintf(&b, "  keep    %-8s %s\n", c.Component, c.Wants)
		}
		b.WriteString("  nothing to do: this machine already matches the file\n")
		return b.String()
	}
	for _, c := range d.Changes {
		switch c.Verb() {
		case "add":
			fmt.Fprintf(&b, "  add     %-8s as a %s install\n", c.Component, c.Wants)
		case "remove":
			fmt.Fprintf(&b, "  remove  %-8s (installed as a %s)\n", c.Component, c.Was)
		case "change":
			fmt.Fprintf(&b, "  change  %-8s %s -> %s\n", c.Component, c.Was, c.Wants)
		default:
			fmt.Fprintf(&b, "  keep    %-8s %s\n", c.Component, c.Wants)
		}
	}
	return b.String()
}

// Elsewhere is the part of the plan this machine will not carry out: what
// every other machine in the file has to run, as the command to run there.
// Printed, never executed -- no machine in this design reaches another.
func Elsewhere(t *Topology, path, self string) string {
	others := t.Others(self)
	if len(others) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nOn the other machines in this file, run there:\n")
	for _, m := range others {
		var parts []string
		for _, c := range m.Components {
			parts = append(parts, c.Kind+" as a "+c.Shape)
		}
		fmt.Fprintf(&b, "  %-10s %s\n", m.Name, strings.Join(parts, ", "))
		fmt.Fprintf(&b, "             inbuxa apply -f %s --machine %s\n", path, m.Name)
	}
	b.WriteString("\nNothing here touches them. Copy the file across and run it there.\n")
	return b.String()
}
