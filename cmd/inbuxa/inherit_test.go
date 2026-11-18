// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/discover"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

func shapes() (shapePtrs, *string, *string, *string) {
	s, c, w := "container", "container", "container"
	return shapePtrs{&s, &c, &w}, &s, &c, &w
}

func recorded() *state.State {
	return &state.State{
		Dir: "/var/lib/inbuxa", Domain: "example.com", MailHost: "mail.example.com",
		Shapes: map[string]string{"server": "host", "console": "container"},
		Proxy:  "snippets", ConsoleHost: "admin.example.com", WebmailHost: "webmail.example.com",
		ACMEEmail: "postmaster@example.com",
	}
}

// #84: a re-run with only new names keeps every other option it was installed with.
func TestInheritKeepsWhatWasNotGiven(t *testing.T) {
	var o plan.Options
	sh, server, console, webmail := shapes()
	o.MailHost = "mail.example.net"
	from := inherit(recorded(), discover.Found{}, map[string]bool{"mail-host": true}, &o, sh)

	if *server != "host" || *console != "container" || *webmail != "skip" {
		t.Fatalf("shapes = %s/%s/%s, want host/container/skip", *server, *console, *webmail)
	}
	if o.Proxy != "snippets" || o.Domain != "example.com" || o.ConsoleHost != "admin.example.com" {
		t.Fatalf("options = %+v", o)
	}
	if o.MailHost != "mail.example.net" {
		t.Fatalf("a given flag was overridden: mail-host %q", o.MailHost)
	}
	for _, i := range from {
		if i.Flag == "mail-host" {
			t.Fatal("mail-host was given, so it isn't inherited")
		}
	}
}

func TestInheritLeavesGivenShapesAlone(t *testing.T) {
	var o plan.Options
	sh, server, _, webmail := shapes()
	*server = "container"
	*webmail = "host"
	inherit(recorded(), discover.Found{}, map[string]bool{"server": true, "webmail": true}, &o, sh)
	if *server != "container" || *webmail != "host" {
		t.Fatalf("given shapes changed: %s %s", *server, *webmail)
	}
}

func TestInheritNothingOnAFreshOrJoinedMachine(t *testing.T) {
	var o plan.Options
	sh, server, _, _ := shapes()
	if from := inherit(&state.State{Shapes: map[string]string{}}, discover.Found{}, nil, &o, sh); from != nil {
		t.Fatalf("fresh machine inherited %v", from)
	}
	joined := &state.State{ServerURL: "https://mail.example.com", Shapes: map[string]string{"console": "container"}}
	if from := inherit(joined, discover.Found{}, nil, &o, sh); from != nil || *server != "container" {
		t.Fatalf("joined front ends inherited %v", from)
	}
}

// An install from before #84 has no proxy or front-end names in its record.
func TestInheritReadsOlderInstallsFromTheMachine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proxy", "nginx-inbuxa.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st := &state.State{Dir: dir, Domain: "example.com", Shapes: map[string]string{"server": "container"}}
	found := discover.Found{Dir: dir, MailHost: "mx.example.com", ConsoleHost: "console.example.org"}
	var o plan.Options
	sh, _, _, _ := shapes()
	inherit(st, found, nil, &o, sh)
	if o.Proxy != "snippets" || o.MailHost != "mx.example.com" || o.ConsoleHost != "console.example.org" {
		t.Fatalf("options = %+v", o)
	}
	if got := recordedProxy(&state.State{}, discover.Found{Dir: dir, Proxy: true}); got != "caddy" {
		t.Fatalf("compose with caddy: %q", got)
	}
}

func TestInheritLocalCarriesNoNames(t *testing.T) {
	st := &state.State{Dir: "/var/lib/inbuxa", Domain: "example.test", Local: true, Shapes: map[string]string{"server": "container"}}
	var o plan.Options
	sh, _, _, _ := shapes()
	inherit(st, discover.Found{MailHost: "mail.example.test"}, nil, &o, sh)
	if !o.Local || o.MailHost != "" || o.Proxy != "" {
		t.Fatalf("options = %+v", o)
	}
}

func TestAnswerKeysCountAsGiven(t *testing.T) {
	a, err := parseAnswers([]byte(`{"proxy":"caddy","server":"host"}`))
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, k := range answerKeys(a) {
		set[k] = true
	}
	if !set["proxy"] || !set["server"] || set["domain"] {
		t.Fatalf("set = %v", set)
	}
}
