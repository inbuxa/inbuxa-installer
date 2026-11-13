// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// manCmd writes the manual page, in roff, to standard output. It is generated
// from the same command model as the completions, so `inbuxa man > inbuxa.1`
// at release time stays in step with the commands. Install it with, e.g.,
// `inbuxa man | sudo tee /usr/share/man/man1/inbuxa.1`.
func manCmd(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(os.Stderr, "usage: inbuxa man   # writes the manual page in roff to stdout")
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "inbuxa man takes no arguments")
		return 2
	}
	fmt.Print(manPage())
	return 0
}

func manPage() string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	date := time.Now().UTC().Format("2006-01-02")
	p(".TH INBUXA 1 %q %q \"inbuxa manual\"", date, "inbuxa "+version)
	p(".SH NAME")
	p("inbuxa \\- install the inbuxa mail suite on this machine")
	p(".SH SYNOPSIS")
	p(".B inbuxa")
	p("[\\fICOMMAND\\fR] [\\fIFLAGS\\fR]")
	p(".SH DESCRIPTION")
	p("Run with no arguments, \\fBinbuxa\\fR opens an interface that fills in the")
	p("machine's own facts in front of you. Run with a command it does the same")
	p("work without asking. It installs the server, the console and the webmail,")
	p("each in a container or on the host, and looks after certificates, DNS and")
	p("upgrades afterwards.")
	p(".SH COMMANDS")
	for _, c := range commands() {
		head := c.name
		if c.arg == "url" {
			head += " URL"
		}
		if c.arg == "component" {
			head += " [component...]"
		}
		if len(c.subs) > 0 {
			head += " " + subBar(c)
		} else if len(c.flags) > 0 {
			head += " [flags]"
		}
		p(".TP")
		p(".B %s", roff(head))
		p("%s.", roff(strings.TrimRight(c.desc, ".")))
		if len(c.subs) > 0 {
			p(".RS")
			for _, s := range c.subs {
				p(".TP")
				p(".B %s", roff(s.name))
				desc := roff(strings.TrimRight(s.desc, "."))
				if len(s.flags) > 0 {
					desc += ". Flags: " + roff(strings.Join(flagTokens(s.flags), ", "))
				} else {
					desc += "."
				}
				p("%s", desc)
			}
			p(".RE")
		} else if len(c.flags) > 0 {
			p(".br")
			p("Flags: %s", roff(strings.Join(flagTokens(c.flags), ", ")))
		}
	}
	p(".SH FILES")
	p(".TP")
	p(".I /etc/inbuxa/install.json")
	p("What is installed on this machine, as inbuxa recorded it.")
	p(".SH SEE ALSO")
	p(".BR inbuxa\\-cli (1),")
	p(".BR inbuxa\\-migrate (1)")
	p(".PP")
	p("Full documentation at \\fIhttps://docs.inbuxa.org/\\fR, and verifying a")
	p("download at \\fIhttps://docs.inbuxa.org/install/verifying/\\fR.")
	p(".SH COPYRIGHT")
	p("Copyright \\(co 2026 Coffey Labs LLC. License AGPL\\-3.0\\-or\\-later.")
	return b.String()
}

func subBar(c command) string {
	out := make([]string, len(c.subs))
	for i, s := range c.subs {
		out[i] = s.name
	}
	return strings.Join(out, "|")
}

// roff escapes text for a roff line: a backslash becomes \e, and a leading dash
// of an option is written \- so it renders as a minus. Hyphens inside a word
// render as hyphens, which is what we want for names like mail-host.
func roff(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\e")
	// Escape the leading dashes of option tokens so mandoc is happy.
	s = strings.ReplaceAll(s, "--", "\\-\\-")
	s = strings.ReplaceAll(s, " -", " \\-")
	return s
}
