// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
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
