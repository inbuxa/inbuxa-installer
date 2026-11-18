// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upgrade takes its components and its options in any order; with nothing
// installed it stops at that, so what is checked is that the options were
// understood, not taken for components.
func TestUpgradeTakesOptionsAfterComponents(t *testing.T) {
	if _, err := os.Stat(filepath.Join("/etc/inbuxa", "install.json")); err == nil {
		t.Skip("an install is recorded on this machine")
	}
	for _, args := range [][]string{
		{"server", "--no-backup", "--dry-run"},
		{"--dry-run", "server", "console"},
		{"webmail", "--dry-run", "--no-backup"},
		{"--check"},
		{"server", "--check"},
		{"--key", "k", "--dry-run"},
	} {
		if code := upgradeCmd(args); code == 2 {
			t.Errorf("upgrade %v: taken as a usage error", args)
		}
	}
	if code := upgradeCmd([]string{"server", "--bogus"}); code != 2 {
		t.Errorf("an unknown option was accepted: %d", code)
	}
	if code := upgradeCmd([]string{"database"}); code != 2 {
		t.Errorf("an unknown component was accepted: %d", code)
	}
}

// dns understands its flags: a bad one is a usage error, the good ones are
// not. With nothing installed it stops before touching the server, so what is
// checked is that the options parsed, not what they would have done.
func TestDNSTakesItsFlags(t *testing.T) {
	if _, err := os.Stat(filepath.Join("/etc/inbuxa", "install.json")); err == nil {
		t.Skip("an install is recorded on this machine")
	}
	for _, args := range [][]string{
		nil,
		{"--refresh", "--domain", "example.org"},
		{"--refresh", "--key", ""},
	} {
		if code := dnsCmd(args); code == 2 {
			t.Errorf("dns %v: taken as a usage error", args)
		}
	}
	if code := dnsCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("an unknown option was accepted: %d", code)
	}
}

// "--key -" with piped input reads the first line as it comes, prompts for
// nothing, and says which secret it couldn't read (#75).
func TestReadSecretLine(t *testing.T) {
	got, err := readSecretLine(strings.NewReader("  abc123 \nsecond\n"), "API key")
	if err != nil || got != "abc123" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := readSecretLine(strings.NewReader("nonewline"), "API key"); err != nil || got != "nonewline" {
		t.Fatalf("no newline: got %q, %v", got, err)
	}
	_, err = readSecretLine(strings.NewReader(""), "Relay token")
	if err == nil || !strings.Contains(err.Error(), "reading the token from standard input") {
		t.Fatalf("empty input: %v", err)
	}
	if got, err := readSecret("given", "API key"); err != nil || got != "given" {
		t.Fatalf("a value other than - is used as given: %q, %v", got, err)
	}
}
