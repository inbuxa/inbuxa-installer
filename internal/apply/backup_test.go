// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"strings"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

func TestBackupKind(t *testing.T) {
	removed := time.Now()
	cases := []struct {
		name       string
		st         *state.State
		wantKind   string
		wantRefuse string // a substring the refusal must mention, "" if it should not refuse
	}{
		{
			name:     "container server",
			st:       &state.State{Dir: "/var/lib/inbuxa", Shapes: map[string]string{"server": "container"}},
			wantKind: "container",
		},
		{
			name:     "host server",
			st:       &state.State{Dir: "/var/lib/inbuxa", Shapes: map[string]string{"server": "host"}},
			wantKind: "host",
		},
		{
			name:       "front ends only, server elsewhere",
			st:         &state.State{Shapes: map[string]string{"webmail": "container"}, ServerURL: "https://mail.example.org"},
			wantRefuse: "another machine",
		},
		{
			name:       "no server here",
			st:         &state.State{Shapes: map[string]string{"webmail": "container"}},
			wantRefuse: "not installed",
		},
		{
			name:       "server skipped",
			st:         &state.State{Shapes: map[string]string{"server": "skip"}},
			wantRefuse: "not installed",
		},
		{
			name:       "uninstalled, data kept",
			st:         &state.State{Shapes: map[string]string{"server": "container"}, RemovedAt: &removed},
			wantRefuse: "nothing is running",
		},
		{
			name:       "clustered node",
			st:         &state.State{Dir: "/var/lib/inbuxa", Shapes: map[string]string{"server": "container"}, Clustered: true},
			wantRefuse: "shared store",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, refuse := BackupKind(tc.st)
			if tc.wantRefuse != "" {
				if refuse == "" {
					t.Fatalf("expected a refusal mentioning %q, got kind %q and no refusal", tc.wantRefuse, kind)
				}
				if kind != "" {
					t.Errorf("a refusal should give no kind, got %q", kind)
				}
				if !strings.Contains(refuse, tc.wantRefuse) {
					t.Errorf("refusal %q does not mention %q", refuse, tc.wantRefuse)
				}
				return
			}
			if refuse != "" {
				t.Fatalf("did not expect a refusal, got %q", refuse)
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
		})
	}
}
