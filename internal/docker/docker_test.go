// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import "testing"

func TestEnvValue(t *testing.T) {
	env := "PATH=/usr/local/bin:/usr/bin\nNODE_ENV=production\nIHASMAIL_VERSION=2026.9.13+pr344\nEMPTY=\n"
	for name, want := range map[string]string{
		"IHASMAIL_VERSION": "2026.9.13+pr344",
		"NODE_ENV":         "production",
		"EMPTY":            "",
		"MISSING":          "",
		"IHASMAIL":         "", // a prefix of a name is not the name
	} {
		if got := envValue(env, name); got != want {
			t.Errorf("envValue(%q) = %q, want %q", name, got, want)
		}
	}
}
