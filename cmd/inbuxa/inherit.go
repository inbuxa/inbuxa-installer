// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/discover"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// inherited is one option taken from what is installed rather than from
// the command line, for the plan to say where it came from.
type inherited struct {
	Flag, Value string
}

// shapePtrs are where install keeps the three shapes while it parses.
type shapePtrs struct {
	server, console, webmail *string
}

// inherit fills every option install was not given (set holds the flags on
// the command line and the keys of an answers file) from the install
// recorded here, so running install again changes only what it is told to
// (#84). Without it an option left out went back to its default: a missing
// `--server host` asked for a move, a missing `--proxy snippets` swapped in
// the managed Caddy. A component the record doesn't list stays skipped.
//
// What installs before 2026.11.18 didn't record (the proxy, the front ends'
// names) is read back from the deployment. Front ends joined to a server
// elsewhere are `inbuxa join`'s, not install's, so they inherit nothing.
func inherit(st *state.State, found discover.Found, set map[string]bool, o *plan.Options, sh shapePtrs) []inherited {
	if st == nil || !st.Installed() || st.ServerURL != "" {
		return nil
	}
	var out []inherited
	str := func(flag string, dst *string, values ...string) {
		if set[flag] {
			return
		}
		for _, v := range values {
			if v != "" {
				*dst = v
				out = append(out, inherited{flag, v})
				return
			}
		}
	}

	if !set["local"] && st.Local {
		o.Local = true
		out = append(out, inherited{"local", "true"})
	}
	str("dir", &o.Dir, st.Dir)
	str("domain", &o.Domain, st.Domain)
	for _, c := range []struct {
		flag string
		dst  *string
	}{{"server", sh.server}, {"console", sh.console}, {"webmail", sh.webmail}} {
		if set[c.flag] {
			continue
		}
		v := st.Shapes[c.flag]
		if v == "" {
			v = string(plan.Skip)
		}
		*c.dst = v
		out = append(out, inherited{c.flag, v})
	}
	if o.Local {
		// Loopback: no names, no proxy, no certificates to carry over.
		return out
	}
	str("mail-host", &o.MailHost, st.MailHost, found.MailHost)
	str("console-host", &o.ConsoleHost, st.ConsoleHost, found.ConsoleHost)
	str("webmail-host", &o.WebmailHost, st.WebmailHost, found.WebmailHost)
	str("proxy", &o.Proxy, st.Proxy, recordedProxy(st, found))
	str("email", &o.ACMEEmail, st.ACMEEmail)
	str("acme-directory", &o.ACMEDirectory, st.ACMEDirectory)
	str("acme-ca-root", &o.ACMECARoot, st.ACMECARoot)
	return out
}

// recordedProxy is the proxy an install from before #84 was made with, as
// its files show it: the managed Caddy in the compose file, the nginx files
// --proxy snippets writes, or neither.
func recordedProxy(st *state.State, found discover.Found) string {
	switch {
	case found.Proxy:
		return "caddy"
	case st.Dir != "" && fileExists(filepath.Join(st.Dir, "proxy", "nginx-inbuxa.conf")):
		return "snippets"
	case found.Dir != "":
		return "none"
	}
	return ""
}

// answerKeys are the options an answers file gave, which count as given.
func answerKeys(a answersFile) []string {
	var keys []string
	add := func(name string, given bool) {
		if given {
			keys = append(keys, name)
		}
	}
	add("domain", a.Domain != nil)
	add("mail-host", a.MailHost != nil)
	add("console-host", a.ConsoleHost != nil)
	add("webmail-host", a.WebmailHost != nil)
	add("email", a.Email != nil)
	add("proxy", a.Proxy != nil)
	add("dir", a.Dir != nil)
	add("local", a.Local != nil)
	add("acme-directory", a.ACMEDirectory != nil)
	add("acme-ca-root", a.ACMECARoot != nil)
	add("server", a.Server != nil)
	add("console", a.Console != nil)
	add("webmail", a.Webmail != nil)
	return keys
}

// inheritedText is the plan's note on what came from the install here.
func inheritedText(from []inherited) string {
	var b strings.Builder
	b.WriteString("Already installed here, so options not given are the install's own (give one to change it):\n")
	for _, i := range from {
		fmt.Fprintf(&b, "  --%-15s %s\n", i.Flag, i.Value)
	}
	b.WriteString("\n")
	return b.String()
}
