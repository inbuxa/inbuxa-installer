// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/compose"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

func TestSetAPIBaseReplacesOrInserts(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.html")
	os.WriteFile(index, []byte(`<html><head><meta name="api-base-url" content="http://old"><title>x</title></head></html>`), 0o644)
	if err := setAPIBase(index, "https://mail.example.org"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(index)
	if strings.Count(string(b), "api-base-url") != 1 || !strings.Contains(string(b), `content="https://mail.example.org"`) {
		t.Errorf("replace: %s", b)
	}
	os.WriteFile(index, []byte(`<html><head><title>x</title></head></html>`), 0o644)
	setAPIBase(index, "https://mail.example.org")
	b, _ = os.ReadFile(index)
	if !strings.Contains(string(b), `<head><meta name="api-base-url" content="https://mail.example.org">`) {
		t.Errorf("insert: %s", b)
	}
}

func TestTheWebmailOnTheHostReachesALocalServerOnLoopback(t *testing.T) {
	o := plan.Options{Dir: "/var/lib/inbuxa", Domain: "example.org"}
	s := compose.Stack{Server: true, WebmailURL: "https://webmail.example.org", ConsoleURL: "https://admin.example.org"}
	env := webmailEnv(o, s, compose.Secrets{AppSecret: "a", WebmailOAuth: "w"}, "2026.10.12+gabc")
	for k, want := range map[string]string{
		"MAIL_SERVER_URL": "http://127.0.0.1:8081", "HOST": "127.0.0.1", "PORT": "8080",
		"STATIC_DIR": "/var/lib/inbuxa/webmail/web/dist", "IHASMAIL_VERSION": "2026.10.12+gabc",
		"OAUTH_CLIENT_SECRET": "w", "PUSH_URL": "https://webmail.example.org", "ADMIN_URL": "https://admin.example.org",
		"IMMUTABLE": "1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if v, ok := env["SESSION_FILE"]; !ok || v != "" {
		t.Error("SESSION_FILE must be there and empty: sessions in memory")
	}
	s.RemoteServerURL = "https://mail.example.org/"
	if got := webmailEnv(o, s, compose.Secrets{}, "")["MAIL_SERVER_URL"]; got != "https://mail.example.org" {
		t.Errorf("joined: MAIL_SERVER_URL = %q", got)
	}
}

func TestUnpackReplacesOnlyWhenComplete(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "src")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "f"), []byte("new"), 0o600)
	tarball := filepath.Join(work, "t.tar.gz")
	if out, err := exec.Command("tar", "-czf", tarball, "-C", src, ".").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	dest := filepath.Join(work, "dest")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "old"), []byte("old"), 0o644)
	if err := unpack(context.Background(), tarball, dest, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "old")); err == nil {
		t.Error("the previous version's files survived")
	}
	if fi, err := os.Stat(filepath.Join(dest, "sub", "f")); err != nil || fi.Mode().Perm()&0o044 != 0o044 {
		t.Errorf("not readable by all: %v %v", fi, err)
	}
	if !unpacked(dest, "abc") || unpacked(dest, "def") {
		t.Error("the release marker is wrong")
	}
	if err := unpack(context.Background(), filepath.Join(work, "missing.tar.gz"), dest, "zzz"); err == nil {
		t.Error("a bad tarball was accepted")
	}
	if !unpacked(dest, "abc") {
		t.Error("a failed unpack disturbed what was there")
	}
}

// The webmail's client is registered from the server's environment: a
// webmail in either shape needs its secret there, or it answers and signs
// nobody in. (2026.10.12.3 left it out for a webmail on the host beside a
// server on the host.)
func TestAServerOnTheHostIsGivenTheWebmailsSecretInEitherShape(t *testing.T) {
	sec := compose.Secrets{WebmailOAuth: "w"}
	o := plan.Options{Dir: "/var/lib/inbuxa"}
	for _, s := range []compose.Stack{
		{HostServer: true, Webmail: true, WebmailURL: "https://webmail.example.org"},
		{HostServer: true, WebmailUnit: true, WebmailURL: "https://webmail.example.org"},
	} {
		env := serverEnv(o, s, sec)
		if env["INBUXA_WEBMAIL_CLIENT_SECRET"] != "w" || env["INBUXA_WEBMAIL_URL"] != "https://webmail.example.org" {
			t.Errorf("webmail container=%v unit=%v: %v", s.Webmail, s.WebmailUnit, env)
		}
	}
	if env := serverEnv(o, compose.Stack{HostServer: true}, sec); env["INBUXA_WEBMAIL_CLIENT_SECRET"] != "" {
		t.Errorf("no webmail, and a secret anyway: %v", env)
	}
}
