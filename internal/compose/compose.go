// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package compose writes the deployment: a compose file, a Caddyfile, and the
// .env beside them that holds the secrets those two refer to.
//
// The files are the deployment. Once they are written, `docker compose up -d`
// in that directory is the whole of it, with or without this installer -- an
// operator who never runs `inbuxa` again still has something they can read,
// edit and bring up by hand.
package compose

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Stack is everything the two templates need. It is flat on purpose: a
// template that reaches through three structs to find a hostname is a
// template nobody can check against the file it produces.
type Stack struct {
	Version string
	Project string
	Domain  string
	Email   string
	Local   bool

	MailHost    string
	ConsoleHost string
	WebmailHost string

	Console bool
	Webmail bool
	Proxy   bool

	ServerImage  string
	ConsoleImage string
	WebmailImage string
	CaddyImage   string

	ServerBind  string // host address for the server's plain HTTP
	ConsoleBind string
	WebmailBind string

	Subnet    string
	ServerIP  string
	ConsoleIP string
	WebmailIP string
	CaddyIP   string

	MailPorts []int // published as-is: 25, 465, 993, 995, 4190

	// URLs as a browser will use them. Local installs have no proxy and no
	// certificates, so they are the loopback binds instead.
	ServerPublicURL string
	ConsoleURL      string
	WebmailURL      string

	ACMEDirectory string
	ACMECARoot    string
	CABundle      bool
}

// ServerNames is every name the server's certificate and the proxy need:
// the mail host, and the four service names the server puts in its own
// certificate by default.
//
// The list has to match what the server asks for exactly. Leaving
// ua-auto-config out of the proxy meant its HTTP-01 challenge was not
// forwarded, the whole order failed on that one name, and the mail ports
// served a self-signed certificate while every other name validated.
func (s Stack) ServerNames() []string {
	names := []string{s.MailHost}
	for _, n := range []string{"autoconfig", "autodiscover", "mta-sts", "ua-auto-config"} {
		names = append(names, n+"."+s.Domain)
	}
	return names
}

// Addresses fills in the private network from the subnet, so the compose file
// has fixed addresses rather than whatever order the daemon started things in.
func (s *Stack) Addresses() error {
	ip, network, err := net.ParseCIDR(s.Subnet)
	if err != nil {
		return fmt.Errorf("subnet %q: %w", s.Subnet, err)
	}
	ip = ip.To4()
	if ip == nil {
		return fmt.Errorf("subnet %q is not IPv4", s.Subnet)
	}
	at := func(n byte) string {
		a := make(net.IP, len(ip))
		copy(a, ip)
		a[3] += n
		if !network.Contains(a) {
			return ""
		}
		return a.String()
	}
	s.ServerIP, s.ConsoleIP, s.WebmailIP, s.CaddyIP = at(10), at(11), at(12), at(13)
	if s.ServerIP == "" || s.CaddyIP == "" {
		return fmt.Errorf("subnet %q is too small for the stack", s.Subnet)
	}
	return nil
}

// Write renders the deployment into dir. It returns the secrets it generated,
// because the caller has to hand one of them to the server as well.
type Secrets struct {
	AppSecret    string // the webmail's session key
	WebmailOAuth string // the first-party client secret both sides hold
}

// Write takes a pointer because it fills in the private addresses as it
// goes, and the caller needs them: the mail server is told which addresses
// the front ends speak from, and passing a copy meant telling it "".
func Write(dir string, s *Stack) (Secrets, error) {
	var sec Secrets
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return sec, err
	}
	if err := s.Addresses(); err != nil {
		return sec, err
	}

	var err error
	if sec.AppSecret, err = secret(); err != nil {
		return sec, err
	}
	if sec.WebmailOAuth, err = secret(); err != nil {
		return sec, err
	}

	funcs := template.FuncMap{"join": strings.Join}
	for name, out := range map[string]string{
		"compose.yaml.tmpl": "compose.yaml",
		"Caddyfile.tmpl":    "Caddyfile",
	} {
		if out == "Caddyfile" && !s.Proxy {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(templates, "templates/"+name)
		if err != nil {
			return sec, err
		}
		var b strings.Builder
		if err := t.Execute(&b, s); err != nil {
			return sec, fmt.Errorf("%s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, out), []byte(b.String()), 0o640); err != nil {
			return sec, err
		}
	}

	// The secrets live beside the compose file, not in it, so the compose
	// file can be read to somebody, pasted into an issue, or committed.
	env := fmt.Sprintf("APP_SECRET=%s\nWEBMAIL_CLIENT_SECRET=%s\n", sec.AppSecret, sec.WebmailOAuth)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		return sec, err
	}
	return sec, nil
}

func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
