// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestManPage(t *testing.T) {
	m := manPage()
	for _, want := range []string{
		".TH INBUXA 1 ",
		".SH NAME",
		"inbuxa \\- install the inbuxa mail suite",
		".SH SYNOPSIS",
		".SH DESCRIPTION",
		".SH COMMANDS",
		".SH FILES",
		"/etc/inbuxa/install.json",
		".SH SEE ALSO",
		"inbuxa\\-cli (1)",
		"AGPL\\-3.0\\-or\\-later",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("man page is missing %q", want)
		}
	}
	// Every command gets a .TP entry; check a representative spread.
	for _, name := range []string{"install", "join", "backup", "cluster", "relay", "completions", "man"} {
		if !strings.Contains(m, ".B "+name) {
			t.Errorf("man page has no entry for %q", name)
		}
	}
	// Option dashes are escaped for roff.
	if !strings.Contains(m, "\\-\\-domain") {
		t.Errorf("man page did not escape the dashes of --domain")
	}
	// No control line may start with a bare backslash-less stray; the page
	// must at least be non-trivial.
	if strings.Count(m, "\n") < 40 {
		t.Errorf("man page looks too short: %d lines", strings.Count(m, "\n"))
	}
}

func TestManCmdArgs(t *testing.T) {
	if code := manCmd(nil); code != 0 {
		t.Errorf("man with no args should exit 0, got %d", code)
	}
	if code := manCmd([]string{"extra"}); code != 2 {
		t.Errorf("man with an argument should exit 2, got %d", code)
	}
}
