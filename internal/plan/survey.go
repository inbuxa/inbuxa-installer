// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package plan

import (
	"fmt"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

// SurveyText is the first screen, as text: what this machine is, and what that
// allows. Every line is something a later choice depends on.
func SurveyText(f host.Facts) string {
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
		if !f.Glibc.AtLeast(ServerGlibcMajor, ServerGlibcMinor) {
			glibc += fmt.Sprintf(" (older than the %d.%d the server binary needs)", ServerGlibcMajor, ServerGlibcMinor)
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
	for _, c := range []Component{Server, Console, Webmail} {
		var shapes []string
		for _, s := range []Shape{Container, Host} {
			switch a := Available(f, c, s); {
			case a.OK:
				shapes = append(shapes, string(s))
			case fixable(f, c, s):
				// Missing, not impossible: what --install-deps adds, said as
				// the offer it is, as the interface says it.
				shapes = append(shapes, fmt.Sprintf("%s (with --install-deps: %s)", s, a.Why))
			default:
				shapes = append(shapes, fmt.Sprintf("%s (no: %s)", s, a.Why))
			}
		}
		fmt.Fprintf(&b, "  %-10s %s\n", c, strings.Join(shapes, "   "))
	}
	return b.String()
}

// fixable is whether a shape this machine can't take as it is becomes one it
// can once the installer adds what is missing: a plan with --install-deps
// builds.
func fixable(f host.Facts, c Component, s Shape) bool {
	_, err := Build(f, Options{Domain: "example.com", InstallDeps: true, Choices: []Choice{{Component: c, Shape: s}}})
	return err == nil
}

func yes(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
