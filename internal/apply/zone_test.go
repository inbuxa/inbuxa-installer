// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"strings"
	"testing"
)

const savedZone = `example.com. 3600 IN MX 10 mail.example.com.
_imaps._tcp.example.com. 3600 IN SRV 0 1 993 mail.example.com.
example.com. 3600 IN TXT "v=spf1 a:mail.example.com -all"
_dmarc.example.com. 3600 IN TXT "v=DMARC1; p=reject; rua=mailto:postmaster@example.com"
`

// #83: a zone saved before a rename doesn't name the new mail host.
func TestZoneNamesHost(t *testing.T) {
	if !ZoneNamesHost(savedZone, "mail.example.com") || !ZoneNamesHost(savedZone, "MAIL.example.com.") {
		t.Fatal("the zone names its own mail host")
	}
	if ZoneNamesHost(savedZone, "mail.example.net") {
		t.Fatal("a renamed mail host isn't in the old zone")
	}
	// A name that only contains the host, or is contained in it, doesn't count.
	if ZoneNamesHost(savedZone, "example") || ZoneNamesHost(savedZone, "ail.example.com") {
		t.Fatal("matched part of a name")
	}
	// An MX at a relay still names the server in its service records.
	relayed := strings.Replace(savedZone, "MX 10 mail.example.com.", "MX 10 edge.example.org.", 1)
	if !ZoneNamesHost(relayed, "mail.example.com") {
		t.Fatal("MX at a relay was taken for a stale zone")
	}
	// SPF alone counts too (a:host).
	if !ZoneNamesHost(`example.com. IN TXT "v=spf1 a:mx.example.com -all"`, "mx.example.com") {
		t.Fatal("SPF a: names the host")
	}
	if !ZoneNamesHost(savedZone, "") {
		t.Fatal("no recorded mail host: nothing to compare")
	}
}
