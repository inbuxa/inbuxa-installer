// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package pins

import "testing"

func TestParseAcceptsTheShapesReleaseUses(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"27.1", Version{Major: 27, Minor: 1, Final: true}},
		{"v27.1", Version{Major: 27, Minor: 1, Final: true}},
		{"27.1.2", Version{Major: 27, Minor: 1, Patch: 2, Final: true}},
		{"27.1-rc.15", Version{Major: 27, Minor: 1, RC: 15}},
		{" 27.1-rc.3 ", Version{Major: 27, Minor: 1, RC: 3}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "27", "27.1.2.3", "27.x", "27.1-beta.1", "27.1-rc.x"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted a bad version", in)
		}
	}
}

func TestCompareOrdersCandidatesBeforeTheFinal(t *testing.T) {
	order := []string{"27.1-rc.1", "27.1-rc.2", "27.1-rc.15", "27.1", "27.2-rc.1", "27.2"}
	for i := 0; i+1 < len(order); i++ {
		a := mustParse(t, order[i])
		b := mustParse(t, order[i+1])
		if a.Compare(b) >= 0 {
			t.Errorf("%s should be older than %s", order[i], order[i+1])
		}
		if b.Compare(a) <= 0 {
			t.Errorf("%s should be newer than %s", order[i+1], order[i])
		}
	}
	if v := mustParse(t, "27.1"); v.Compare(v) != 0 {
		t.Errorf("a version should equal itself")
	}
}

func TestStringRoundTrips(t *testing.T) {
	for _, s := range []string{"27.1", "27.1.2", "27.1-rc.15"} {
		if got := mustParse(t, s).String(); got != s {
			t.Errorf("String() = %q, want %q", got, s)
		}
	}
}

func TestCheckFindsANewerCandidate(t *testing.T) {
	c := Component{Name: "server", Current: "27.1-rc.15", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v27.1-rc.14", Prerelease: true},
		{Tag: "v27.1-rc.16", Prerelease: true},
		{Tag: "v27.1-rc.15", Prerelease: true},
	}
	f, stale, err := Check(c, rels)
	if err != nil || !stale {
		t.Fatalf("Check: stale=%v err=%v", stale, err)
	}
	if f.Latest != "27.1-rc.16" || f.Current != "27.1-rc.15" {
		t.Errorf("Finding = %+v", f)
	}
}

func TestCandidatePinMovesToTheFinal(t *testing.T) {
	c := Component{Name: "server", Current: "27.1-rc.15", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v27.1-rc.16", Prerelease: true},
		{Tag: "v27.1", Prerelease: false},
	}
	f, stale, err := Check(c, rels)
	if err != nil || !stale {
		t.Fatalf("Check: stale=%v err=%v", stale, err)
	}
	if f.Latest != "27.1" {
		t.Errorf("a candidate pin should move to the final, got %q", f.Latest)
	}
}

func TestFinalPinIgnoresLaterCandidates(t *testing.T) {
	c := Component{Name: "server", Current: "27.1", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v27.2-rc.1", Prerelease: true},
		{Tag: "v27.1", Prerelease: false},
	}
	_, stale, err := Check(c, rels)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Errorf("a final pin must not move onto a later series' candidate")
	}
}

func TestCheckStaysInTheSameSeries(t *testing.T) {
	// A later flagship series is a deliberate move to a new installer line, not
	// a pin bump, even when it is a final.
	c := Component{Name: "server", Current: "27.1", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v27.2-rc.1", Prerelease: true},
		{Tag: "v27.2", Prerelease: false},
	}
	_, stale, err := Check(c, rels)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Errorf("a 27.1 pin must not be pulled onto the 27.2 series")
	}
}

func TestCheckIgnoresTheCalendarVersionedLine(t *testing.T) {
	// The suite ships a parallel calendar-versioned maintenance line
	// (2026.11.11) whose major sorts above the flagship's (2026 > 27). It must
	// not be read as a newer 27.1.
	c := Component{Name: "server", Current: "27.1-rc.15", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v2026.11.11", Prerelease: false},
		{Tag: "v2026.10.26", Prerelease: false},
		{Tag: "v27.1-rc.16", Prerelease: true},
	}
	f, stale, err := Check(c, rels)
	if err != nil || !stale {
		t.Fatalf("Check: stale=%v err=%v", stale, err)
	}
	if f.Latest != "27.1-rc.16" {
		t.Errorf("should move within the 27.1 series only, got %q", f.Latest)
	}
}

func TestCurrentPinIsNotStale(t *testing.T) {
	c := Component{Name: "server", Current: "27.1-rc.16", TagPrefix: "v"}
	rels := []Release{
		{Tag: "v27.1-rc.15", Prerelease: true},
		{Tag: "v27.1-rc.16", Prerelease: true},
	}
	_, stale, err := Check(c, rels)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Errorf("the newest release equals the pin; not stale")
	}
}

func TestCheckHonoursTheTagPrefixAndSkipsOthers(t *testing.T) {
	c := Component{Name: "webmail", Current: "27.1-rc.3", TagPrefix: "inbuxa-v"}
	rels := []Release{
		{Tag: "inbuxa-v27.1-rc.5", Prerelease: true},
		{Tag: "v27.9-rc.9", Prerelease: true}, // wrong prefix: another tag line, ignored
		{Tag: "nightly", Prerelease: true},    // unparseable: skipped, not fatal
	}
	f, stale, err := Check(c, rels)
	if err != nil || !stale {
		t.Fatalf("Check: stale=%v err=%v", stale, err)
	}
	if f.Latest != "27.1-rc.5" {
		t.Errorf("Finding = %+v", f)
	}
}

func TestCheckReportsABadCurrentPin(t *testing.T) {
	c := Component{Name: "server", Current: "not-a-version", TagPrefix: "v"}
	if _, _, err := Check(c, nil); err == nil {
		t.Errorf("a malformed current pin should be an error")
	}
}

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}
