// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stack(hostServer bool) *Stack {
	return &Stack{
		Version: "test", Project: "inbuxa", Domain: "example.com", Email: "postmaster@example.com",
		MailHost: "mail.example.com", ConsoleHost: "admin.example.com", WebmailHost: "webmail.example.com",
		Server: !hostServer, HostServer: hostServer, Console: true, Webmail: true, Proxy: true,
		ServerImage: "s", ConsoleImage: "c", WebmailImage: "w", CaddyImage: "k",
		ServerBind: "127.0.0.1:8081", ConsoleBind: "127.0.0.1:8082", WebmailBind: "127.0.0.1:8080",
		Subnet: "172.31.253.0/24", MailPorts: []int{25},
		ServerPublicURL: "https://mail.example.com", ConsoleURL: "https://admin.example.com", WebmailURL: "https://webmail.example.com",
	}
}

func render(t *testing.T, s *Stack) (compose, caddy string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Write(dir, s); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	b, _ := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	return string(a), string(b)
}

func TestContainerServerIsInTheProject(t *testing.T) {
	c, k := render(t, stack(false))
	for _, want := range []string{"\n  server:\n", "depends_on: [server]", "MAIL_SERVER_URL: http://server:8080", "inbuxa-data:/var/lib/inbuxa"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
	if strings.Contains(c, "network_mode: host") {
		t.Error("a container install put something on the host's network")
	}
	if !strings.Contains(k, "reverse_proxy server:8080") || !strings.Contains(k, "reverse_proxy webmail:8080") {
		t.Errorf("Caddyfile does not use the project's names:\n%s", k)
	}
}

func TestHostServerIsReachedOnLoopback(t *testing.T) {
	s := stack(true)
	c, k := render(t, s)
	for _, bad := range []string{"\n  server:\n", "depends_on", "inbuxa-data:", "http://server:8080"} {
		if strings.Contains(c, bad) {
			t.Errorf("compose.yaml still has %q", bad)
		}
	}
	for _, want := range []string{"network_mode: host", "HOST: 127.0.0.1", "MAIL_SERVER_URL: http://127.0.0.1:8081", "\"127.0.0.1:8082:8080\""} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
	for _, want := range []string{"reverse_proxy 127.0.0.1:8081", "reverse_proxy 127.0.0.1:8080", "reverse_proxy 127.0.0.1:8082"} {
		if !strings.Contains(k, want) {
			t.Errorf("Caddyfile lacks %q", want)
		}
	}
	if s.WebmailIP != "127.0.0.1" || s.CaddyIP != "127.0.0.1" {
		t.Errorf("the server would exempt %s and %s, not loopback", s.WebmailIP, s.CaddyIP)
	}
}

func TestSecretsSurviveARewrite(t *testing.T) {
	dir := t.TempDir()
	a, err := EnsureSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Write(dir, stack(true))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("writing the deployment rolled the secrets")
	}
}
