// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"errors"
	"testing"
)

func TestServiceChecksSkipUnchosenAndFlagNotRunning(t *testing.T) {
	shapes := map[string]string{"server": "container", "console": "host", "webmail": "skip"}
	services := map[string]string{"server": "running", "console": "stopped", "webmail": "absent"}
	got := ServiceChecks(shapes, services)
	// webmail is "skip", so it is not checked; server and console are, in name order.
	if len(got) != 2 {
		t.Fatalf("want 2 checks, got %d: %+v", len(got), got)
	}
	if got[0].Name != "service: console" || got[0].Level != Fail {
		t.Errorf("console should fail (stopped): %+v", got[0])
	}
	if got[1].Name != "service: server" || got[1].Level != OK {
		t.Errorf("server should be ok (running): %+v", got[1])
	}
}

func TestServiceChecksMissingServiceFails(t *testing.T) {
	got := ServiceChecks(map[string]string{"server": "container"}, map[string]string{})
	if len(got) != 1 || got[0].Level != Fail {
		t.Fatalf("a chosen but undiscovered service should fail: %+v", got)
	}
}

func TestCertCheck(t *testing.T) {
	cases := []struct {
		name    string
		local   bool
		err     error
		problem string
		want    Level
	}{
		{"local skips", true, nil, "", Skipped},
		{"probe error fails", false, errors.New("no TLS"), "", Fail},
		{"clean is ok", false, nil, "", OK},
		{"expiring soon warns", false, nil, "it expires in 3 days and has not been renewed", Warn},
		{"expired fails", false, nil, "it expired on 2026-01-01", Fail},
		{"self-signed fails", false, nil, "it is the server's own self-signed certificate, which mail clients refuse", Fail},
		{"wrong name fails", false, nil, "it does not name mail.example.com", Fail},
		{"untrusted fails", false, nil, "it does not verify: x509: unknown authority", Fail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CertCheck(c.local, c.err, c.problem)
			if got.Level != c.want {
				t.Errorf("CertCheck(%v,%v,%q) level = %v, want %v", c.local, c.err, c.problem, got.Level, c.want)
			}
		})
	}
}

func TestPortChecks(t *testing.T) {
	got := PortChecks([]PortProbe{
		{Port: 25, For: "SMTP", Open: true},
		{Port: 993, For: "IMAPS", Open: false, Err: "connection refused"},
	})
	if got[0].Level != OK {
		t.Errorf("open port should be ok: %+v", got[0])
	}
	if got[1].Level != Fail || got[1].Name != "port 993" {
		t.Errorf("closed port should fail: %+v", got[1])
	}
}

func TestDNSChecks(t *testing.T) {
	t.Run("no domain is skipped", func(t *testing.T) {
		got := DNSChecks(DNSFacts{})
		if len(got) != 1 || got[0].Level != Skipped {
			t.Fatalf("want one skipped check: %+v", got)
		}
	})
	t.Run("matching MX and resolving host are ok", func(t *testing.T) {
		got := DNSChecks(DNSFacts{
			Domain: "example.com", MailHost: "mail.example.com.",
			MX: []string{"mail.example.com."}, HostAddrs: []string{"203.0.113.4"},
		})
		for _, c := range got {
			if c.Level != OK {
				t.Errorf("%s should be ok: %+v", c.Name, c)
			}
		}
	})
	t.Run("missing MX and unresolved host warn, not fail", func(t *testing.T) {
		got := DNSChecks(DNSFacts{
			Domain: "example.com", MailHost: "mail.example.com",
			MXErr: "no such host", HostErr: "no such host",
		})
		for _, c := range got {
			if c.Level != Warn {
				t.Errorf("%s should warn: %+v", c.Name, c)
			}
		}
	})
	t.Run("MX pointing elsewhere warns", func(t *testing.T) {
		got := DNSChecks(DNSFacts{
			Domain: "example.com", MailHost: "mail.example.com",
			MX: []string{"aspmx.l.google.com."}, HostAddrs: []string{"203.0.113.4"},
		})
		if got[0].Level != Warn {
			t.Errorf("MX pointing elsewhere should warn: %+v", got[0])
		}
	})
}

func TestReportWorstAndExit(t *testing.T) {
	r := Report{}
	r.Add(Check{"a", OK, ""})
	r.Add(Check{"b", Skipped, ""})
	if r.Worst() != OK || r.ExitCode() != 0 {
		t.Fatalf("ok+skipped should be ok/exit 0: worst=%v exit=%d", r.Worst(), r.ExitCode())
	}
	r.Add(Check{"c", Warn, ""})
	if r.Worst() != Warn || r.ExitCode() != 0 {
		t.Fatalf("a warning is not a failure: worst=%v exit=%d", r.Worst(), r.ExitCode())
	}
	r.Add(Check{"d", Fail, ""})
	if r.Worst() != Fail || r.ExitCode() != 1 {
		t.Fatalf("a failure exits 1: worst=%v exit=%d", r.Worst(), r.ExitCode())
	}
}
