// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package deps

import (
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

func hasNeed(needs []Need, name string) bool {
	for _, n := range needs {
		if n.Name == name {
			return true
		}
	}
	return false
}

// #65: Docker's IPv6 forwarding is offered where Docker relays IPv6 and the
// machine has it, and for a Docker this run installs; never for podman, a
// machine without IPv6, or Docker that already forwards.
func TestDockerIPv6Offered(t *testing.T) {
	const want = "docker IPv6 forwarding"
	relays := host.Facts{IPv6: true, Runtime: host.Runtime{Kind: "docker", Present: true, Usable: true, Version: "26.1.5+dfsg1", Compose: "v2"}}
	if !hasNeed(For(relays, true, false), want) {
		t.Error("not offered for Docker 26 that relays IPv6")
	}
	if hasNeed(For(relays, false, false), want) {
		t.Error("offered with nothing in containers")
	}
	noV6 := relays
	noV6.IPv6 = false
	if hasNeed(For(noV6, true, false), want) {
		t.Error("offered on a machine without IPv6")
	}
	forwards := relays
	forwards.Runtime.ForwardsIPv6 = true
	if hasNeed(For(forwards, true, false), want) {
		t.Error("offered for Docker that already forwards")
	}
	podman := host.Facts{IPv6: true, OS: host.OSInfo{Family: "rhel"}, Runtime: host.Runtime{Kind: "podman", Present: true, Usable: true, ForwardsIPv6: true}}
	if hasNeed(For(podman, true, false), want) {
		t.Error("offered for podman")
	}
	fresh := host.Facts{IPv6: true, OS: host.OSInfo{ID: "debian", Family: "debian"}}
	needs := For(fresh, true, false)
	if !hasNeed(needs, "docker") || !hasNeed(needs, want) {
		t.Errorf("a Debian machine with no runtime: got %v", needs)
	}
	if needs[len(needs)-1].Name != want {
		t.Error("IPv6 forwarding is not offered after Docker itself")
	}
}
