// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package plan

import (
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

func machine() host.Facts {
	return host.Facts{
		Root: true, Systemd: true,
		Runtime: host.Runtime{Kind: "docker", Present: true, Usable: true, Compose: "v2"},
		Glibc:   host.Glibc{Version: "2.41", Major: 2, Minor: 41},
		Node:    host.Node{Present: true, Path: host.OwnPath, Version: "26.11.1", Major: 26},
	}
}

func shapes(server, console, webmail Shape) []Choice {
	return []Choice{{Server, server}, {Console, console}, {Webmail, webmail}}
}

func TestTheConsoleOnTheHostNeedsAWebServer(t *testing.T) {
	t.Setenv("INBUXA_CONSOLE_TARBALL", "/tmp/console.tar.gz")
	f := machine()
	for _, tc := range []struct {
		local bool
		proxy string
		ok    bool
	}{
		{false, "caddy", true}, {false, "snippets", true}, {false, "none", false}, {true, "", false},
	} {
		o := Options{Domain: "example.org", Local: tc.local, Proxy: tc.proxy, Choices: shapes(Container, Host, Container)}
		_, err := Build(f, o)
		if (err == nil) != tc.ok {
			t.Errorf("local=%v proxy=%q: err = %v", tc.local, tc.proxy, err)
		}
		if err != nil && !strings.Contains(err.Error(), "web server") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	}
}

func TestHostFrontEndsWaitForTheirReleases(t *testing.T) {
	f := machine()
	if a := Available(f, Console, Host); a.OK || !strings.Contains(a.Why, "no console release") {
		t.Errorf("console host with no release: %+v", a)
	}
	if a := Available(f, Webmail, Host); a.OK || !strings.Contains(a.Why, "no webmail release") {
		t.Errorf("webmail host with no release: %+v", a)
	}
	t.Setenv("INBUXA_WEBMAIL_TARBALL", "/tmp/webmail.tar.gz")
	if a := Available(f, Webmail, Host); !a.OK {
		t.Errorf("webmail host with a tarball and Node 26: %+v", a)
	}
	f.Node = host.Node{Present: true, Path: "/usr/bin/node", Major: 26}
	if a := Available(f, Webmail, Host); a.OK || !strings.Contains(a.Why, "own Node") {
		t.Errorf("the system's Node was accepted for the webmail: %+v", a)
	}
	// ...and the installer offers to fetch its own
	p, err := Build(f, Options{Domain: "example.org", InstallDeps: true, Choices: shapes(Container, Container, Host)})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Needs) != 1 || p.Needs[0].Name != "node" {
		t.Errorf("needs = %+v", p.Needs)
	}
}
