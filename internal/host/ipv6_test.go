// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import "testing"

// Measured on Debian 13's docker.io 26.1.5 (#65): without ip6tables an IPv6
// client of a published port reached the container as the gateway; with
// "ip6tables" and "experimental" on, as itself; with "ip6tables" alone the
// daemon would not start.
func TestDockerForwardsIPv6(t *testing.T) {
	for _, c := range []struct {
		version, daemon string
		want            bool
	}{
		{"26.1.5+dfsg1", "", false},
		{"26.1.5+dfsg1", `{"ip6tables": true}`, false},
		{"26.1.5+dfsg1", `{"ip6tables": true, "experimental": true}`, true},
		{"27.0.3", "", true},
		{"29.0.1", `{"log-driver": "journald"}`, true},
		{"29.0.1", `{"ip6tables": false}`, false},
		{"", "", false},
	} {
		got, err := DockerForwardsIPv6(c.version, []byte(c.daemon))
		if err != nil || got != c.want {
			t.Errorf("Docker %q with %q: got %v, %v; want %v", c.version, c.daemon, got, err, c.want)
		}
	}
	if _, err := DockerForwardsIPv6("29.0.1", []byte("{not json")); err == nil {
		t.Error("a daemon.json that is not JSON was read as something")
	}
}

func TestDockerMajor(t *testing.T) {
	for v, want := range map[string]int{"26.1.5+dfsg1": 26, "v29.0.1": 29, "28": 28, "": 0, "dev": 0} {
		if got := DockerMajor(v); got != want {
			t.Errorf("DockerMajor(%q) = %d, want %d", v, got, want)
		}
	}
}
