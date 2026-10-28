// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import "testing"

// A binary is the pinned release only when its version is exactly the pin.
// A substring match took 27.1-rc.1 and 27.10.2 for 27.1.
func TestIsVersion(t *testing.T) {
	for _, c := range []struct {
		out, want string
		is        bool
	}{
		{"2026.10.15", "2026.10.15", true},
		{"27.1\n", "27.1", true},
		{"27.1-rc.1", "27.1-rc.1", true},
		{"27.1-rc.1", "27.1", false},
		{"27.10.2", "27.1", false},
		{"27.1.3", "27.1", false},
		{"2026.10.15", "2026.10.1", false},
		{"", "27.1", false},
		{"exec format error: 27.1", "27.1", false},
	} {
		if got := isVersion(c.out, c.want); got != c.is {
			t.Errorf("isVersion(%q, %q) = %v, want %v", c.out, c.want, got, c.is)
		}
	}
}
