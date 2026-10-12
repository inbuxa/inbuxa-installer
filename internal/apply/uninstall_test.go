// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

func TestUninstallKeepsTheDataUnlessPurged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  server:\n  caddy:\n"), 0o640)
	os.MkdirAll(filepath.Join(dir, "backups"), 0o700)
	st := &state.State{Dir: dir, Shapes: map[string]string{"server": "container", "console": "container", "webmail": "container"}}

	r := UninstallPlan(st, false)
	all := func(l []string) string { return strings.Join(l, "\n") }
	if !strings.Contains(all(r.Remove), "containers") {
		t.Errorf("remove = %v", r.Remove)
	}
	for _, want := range []string{"inbuxa_inbuxa-data", "Caddy's volumes", "credentials.txt", "backups", "marked removed"} {
		if !strings.Contains(all(r.Keep), want) {
			t.Errorf("kept lacks %q: %v", want, r.Keep)
		}
	}

	r = UninstallPlan(st, true)
	for _, want := range []string{"inbuxa_inbuxa-data", "Caddy's volumes", "credentials.txt", "the install record"} {
		if !strings.Contains(all(r.Remove), want) {
			t.Errorf("purge does not remove %q: %v", want, r.Remove)
		}
	}
	if !strings.Contains(all(r.Keep), "backups") {
		t.Error("purge would take the backups")
	}
}

func TestUninstallOnTheHost(t *testing.T) {
	st := &state.State{Dir: t.TempDir(), Shapes: map[string]string{"server": "host", "console": "host", "webmail": "host"},
		ServerURL: ""}
	r := UninstallPlan(st, false)
	all := strings.Join(r.Remove, "\n")
	for _, want := range []string{HostServerUnit, HostWebmailUnit, "console's files"} {
		if !strings.Contains(all, want) {
			t.Errorf("remove lacks %q: %v", want, r.Remove)
		}
	}
	if !strings.Contains(strings.Join(r.Keep, "\n"), "/server (the server's configuration and mail)") {
		t.Errorf("the host server's data is not kept: %v", r.Keep)
	}
	st.ServerURL = "https://mail.example.org"
	if r := UninstallPlan(st, false); !strings.Contains(strings.Join(r.Notes, "\n"), "still lists these front ends") {
		t.Errorf("a joined machine's note is missing: %v", r.Notes)
	}
}
