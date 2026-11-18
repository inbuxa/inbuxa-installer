// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package discover

import "testing"

func TestFrontEndHosts(t *testing.T) {
	compose := "services:\n  server:\n    environment:\n      INBUXA_ADMIN_URL: https://admin.example.com\n      INBUXA_WEBMAIL_URL: https://webmail.example.com\n"
	if c, w := FrontEndHosts(compose); c != "admin.example.com" || w != "webmail.example.com" {
		t.Fatalf("compose: %q %q", c, w)
	}
	env := "INBUXA_ADMIN_URL=\"https://console.example.org\"\nINBUXA_WEBMAIL_URL=\n"
	if c, w := FrontEndHosts(env); c != "console.example.org" || w != "" {
		t.Fatalf("server.env: %q %q", c, w)
	}
	if c, _ := FrontEndHosts("INBUXA_ADMIN_URL=http://127.0.0.1:8082\n"); c != "" {
		t.Fatalf("a loopback bind is not a name: %q", c)
	}
}
