// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

func TestParseAnswersReadsFieldsAndRejectsTypos(t *testing.T) {
	a, err := parseAnswers([]byte(`{"domain":"example.com","server":"host","local":false,"install-deps":true}`))
	if err != nil {
		t.Fatalf("valid answers should parse: %v", err)
	}
	if a.Domain == nil || *a.Domain != "example.com" {
		t.Errorf("domain not read: %+v", a.Domain)
	}
	if a.Server == nil || *a.Server != "host" {
		t.Errorf("server not read: %+v", a.Server)
	}
	if a.Local == nil || *a.Local != false {
		t.Errorf("an explicit false should be read, not left nil: %+v", a.Local)
	}
	if a.InstallDeps == nil || *a.InstallDeps != true {
		t.Errorf("install-deps not read: %+v", a.InstallDeps)
	}
	if a.MailHost != nil {
		t.Errorf("a key left out should stay nil, got %+v", a.MailHost)
	}

	if _, err := parseAnswers([]byte(`{"domян":"typo"}`)); err == nil {
		t.Error("an unknown key should be rejected")
	}
	if _, err := parseAnswers([]byte(`{"domain":`)); err == nil {
		t.Error("malformed JSON should error")
	}
}

func TestApplyAnswersFillsUnsetOptions(t *testing.T) {
	a, err := parseAnswers([]byte(`{"domain":"example.com","mail-host":"mx.example.com","server":"host","local":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var o plan.Options
	server, console, webmail := "container", "container", "container"
	applyAnswers(a, map[string]bool{}, &o, &server, &console, &webmail)
	if o.Domain != "example.com" {
		t.Errorf("domain not applied: %q", o.Domain)
	}
	if o.MailHost != "mx.example.com" {
		t.Errorf("mail-host not applied: %q", o.MailHost)
	}
	if server != "host" {
		t.Errorf("server shape not applied: %q", server)
	}
	if !o.Local {
		t.Errorf("local not applied")
	}
	if console != "container" {
		t.Errorf("a key left out must not change a default: console=%q", console)
	}
}

func TestApplyAnswersFlagsWinOverFile(t *testing.T) {
	a, err := parseAnswers([]byte(`{"domain":"from-file.example","server":"host"}`))
	if err != nil {
		t.Fatal(err)
	}
	var o plan.Options
	o.Domain = "from-flag.example" // as if set on the command line
	server := "skip"               // as if --server skip was given
	console, webmail := "container", "container"
	// Both "domain" and "server" were set on the command line.
	applyAnswers(a, map[string]bool{"domain": true, "server": true}, &o, &server, &console, &webmail)
	if o.Domain != "from-flag.example" {
		t.Errorf("a command-line flag must win over the file: domain=%q", o.Domain)
	}
	if server != "skip" {
		t.Errorf("a command-line shape must win over the file: server=%q", server)
	}
}
