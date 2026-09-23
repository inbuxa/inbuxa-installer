// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apply carries out a plan, for the shapes that are containers.
//
// The sequence is SPEC.md 6.2's, which ihasmail-oneshot worked out against a
// running server: bring the mail server up in bootstrap mode with a
// credential that exists only for this step, complete bootstrap, bring the
// rest of the stack up without that credential, exempt the front ends from
// the auto-ban, restart so the settings take, create the first account, and
// write down what nobody can recover later.
//
// Every step is one of the plan's steps, in the plan's order. A step that
// fails stops the run with the service's own last lines, because "compose
// exited 1" is not a reason.
package apply

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/compose"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
)

// Images are the defaults. They are tags rather than digests for now: the
// registry is ours and the tags are immutable releases, and pinning digests
// here would mean this program needing a release of its own for every one of
// theirs.
const (
	DefaultServerImage  = "registry.coffeylabs.org/inbuxa/inbuxa-server:2026.9.23"
	DefaultConsoleImage = "registry.coffeylabs.org/inbuxa/inbuxa-admin:2026.9.21.2"
	DefaultWebmailImage = "registry.coffeylabs.org/inbuxa/ihasmail-inbuxa:2026.9.22-gc2f13d6"
	DefaultCaddyImage   = "docker.io/library/caddy:2-alpine"
	DefaultSubnet       = "172.31.253.0/24"
)

// Result is what the operator is left holding.
type Result struct {
	Dir        string
	AdminUser  string
	AdminPass  string
	FirstUser  string
	FirstPass  string
	ZoneFile   string
	ConsoleURL string
	WebmailURL string
	ServerURL  string
}

// Log is how apply reports progress. The interface will draw ticks from it;
// the flag path prints it.
type Log interface {
	Step(format string, a ...any)
	Info(format string, a ...any)
	Out() io.Writer
}

// Run carries out p. It refuses anything but container shapes for now, and
// says so rather than pretending a host install happened.
func Run(ctx context.Context, p plan.Plan, log Log) (*Result, error) {
	o := p.Options
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		if o.Shape(c) == plan.Host {
			return nil, fmt.Errorf("host installs are not built yet: %s was asked for as a host install", c)
		}
	}
	if o.Shape(plan.Server) != plan.Container {
		return nil, fmt.Errorf("this build installs the front ends only alongside a server it installs too; use join once that is built")
	}

	if len(p.Needs) > 0 && o.InstallDeps {
		log.Step("installing what this machine is missing")
		if err := deps.Resolve(ctx, log.Out(), p.Needs); err != nil {
			return nil, err
		}
	}

	dir := o.Dir
	stack := compose.Stack{
		Version:      "dev",
		Project:      "inbuxa",
		Domain:       o.Domain,
		Email:        o.ACMEEmail,
		Local:        o.Local,
		MailHost:     o.MailHost,
		ConsoleHost:  o.ConsoleHost,
		WebmailHost:  o.WebmailHost,
		Console:      o.Shape(plan.Console) == plan.Container,
		Webmail:      o.Shape(plan.Webmail) == plan.Container,
		Proxy:        !o.Local && o.Proxy == "caddy",
		ServerImage:  DefaultServerImage,
		ConsoleImage: DefaultConsoleImage,
		WebmailImage: DefaultWebmailImage,
		CaddyImage:   DefaultCaddyImage,
		ServerBind:   "127.0.0.1:8081",
		ConsoleBind:  "127.0.0.1:8082",
		WebmailBind:  "127.0.0.1:8080",
		Subnet:       DefaultSubnet,
	}
	if o.Local {
		// Nothing is published and nothing is certified: the addresses are
		// the loopback binds, which is what the front ends are told to use.
		stack.ServerPublicURL = "http://127.0.0.1:8081"
		if stack.Console {
			stack.ConsoleURL = "http://127.0.0.1:8082"
		}
		if stack.Webmail {
			stack.WebmailURL = "http://127.0.0.1:8080"
		}
	} else {
		stack.MailPorts = []int{25, 465, 993, 995, 4190}
		stack.ServerPublicURL = "https://" + o.MailHost
		if stack.Console {
			stack.ConsoleURL = "https://" + o.ConsoleHost
		}
		if stack.Webmail {
			stack.WebmailURL = "https://" + o.WebmailHost
		}
	}

	log.Step("writing the deployment into %s", dir)
	if _, err := compose.Write(dir, &stack); err != nil {
		return nil, err
	}
	log.Info("compose.yaml, .env%s", map[bool]string{true: " and Caddyfile", false: ""}[stack.Proxy])

	cmp := docker.Compose{Dir: dir}

	log.Step("fetching the images")
	if err := cmp.Run(ctx, "pull", "--quiet"); err != nil {
		return nil, err
	}

	// The bootstrap credential lives in an override file for this step only,
	// and its password only in this process. Bringing the stack up afterwards
	// without the override recreates the server without the variable, so no
	// fixed recovery credential outlives the setup.
	bootPass, err := password(24)
	if err != nil {
		return nil, err
	}
	override := filepath.Join(os.TempDir(), "inbuxa-bootstrap.yaml")
	if err := os.WriteFile(override, []byte(
		"services:\n  server:\n    environment:\n      INBUXA_RECOVERY_ADMIN: ${INBUXA_BOOTSTRAP_ADMIN:?}\n"), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(override)

	log.Step("starting the mail server in bootstrap mode")
	boot := cmp
	boot.Files = []string{override}
	boot.Env = []string{"INBUXA_BOOTSTRAP_ADMIN=admin:" + bootPass}
	if err := boot.Run(ctx, "up", "-d", "server"); err != nil {
		return nil, err
	}

	serverURL := "http://" + stack.ServerBind
	if err := waitFor(ctx, log, "the mail server", 120*time.Second, func(ctx context.Context) error {
		return live(ctx, serverURL)
	}); err != nil {
		return nil, withLogs(ctx, err, cmp, "server")
	}

	bootClient := &jmap.Client{BaseURL: serverURL, Username: "admin", Password: bootPass}
	if err := bootClient.CheckBootstrapMode(ctx); err != nil {
		return nil, err
	}

	log.Step("setting up %s (hostname %s)", o.Domain, o.MailHost)
	admin, err := bootClient.Bootstrap(ctx, o.MailHost, o.Domain)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	res := &Result{
		Dir: dir, AdminUser: admin.Username, AdminPass: admin.Secret,
		ConsoleURL: stack.ConsoleURL, WebmailURL: stack.WebmailURL, ServerURL: stack.ServerPublicURL,
	}
	// Written now, not at the end: from this moment the password exists
	// nowhere else, and a failure in a later step must not lose it.
	if err := writeCredentials(dir, res); err != nil {
		return res, err
	}
	log.Info("administrator %s, password in %s", admin.Username, filepath.Join(dir, "credentials.txt"))

	log.Step("starting the rest of the stack")
	if err := cmp.Run(ctx, "up", "-d"); err != nil {
		return res, err
	}

	srv := &jmap.Client{BaseURL: serverURL, Username: admin.Username, Password: admin.Secret}
	if err := waitFor(ctx, log, "the server to come back configured", 120*time.Second, func(ctx context.Context) error {
		_, err := srv.DomainID(ctx, o.Domain)
		return err
	}); err != nil {
		return res, withLogs(ctx, err, cmp, "server")
	}
	domainID, err := srv.DomainID(ctx, o.Domain)
	if err != nil {
		return res, err
	}

	// Every request the webmail makes arrives from one address: every
	// sign-in, every push stream opened and dropped as tabs come and go. The
	// server bans per address, so a ban on that one is a ban on everybody's
	// webmail. The webmail rate-limits sign-ins per real client itself.
	if stack.Webmail {
		log.Step("exempting the webmail from the auto-ban")
		if err := srv.AllowIP(ctx, stack.WebmailIP, "the webmail: every request arrives from this address"); err != nil {
			return res, fmt.Errorf("exempting the webmail: %w", err)
		}
	}
	if stack.Proxy {
		log.Step("trusting the proxy's X-Forwarded-For")
		if err := srv.TrustForwardedFor(ctx); err != nil {
			return res, fmt.Errorf("trusting the proxy: %w", err)
		}
		if err := srv.AllowIP(ctx, stack.CaddyIP, "the proxy: certificate renewals and autoconfig arrive from this address"); err != nil {
			return res, fmt.Errorf("exempting the proxy: %w", err)
		}
	}
	if stack.Webmail || stack.Proxy {
		// Neither setting takes effect on a running server.
		log.Info("restarting the server so those take effect")
		if err := cmp.Run(ctx, "restart", "server"); err != nil {
			return res, err
		}
		if err := waitFor(ctx, log, "the server to restart", 120*time.Second, func(ctx context.Context) error {
			_, err := srv.DomainID(ctx, o.Domain)
			return err
		}); err != nil {
			return res, withLogs(ctx, err, cmp, "server")
		}
	}

	if !o.Local {
		log.Step("turning on certificates")
		if _, err := srv.EnableACME(ctx, domainID, "", o.ACMEEmail); err != nil {
			return res, fmt.Errorf("enabling ACME: %w", err)
		}
	}

	log.Step("creating the first account")
	first := "postmaster"
	pass, err := password(20)
	if err != nil {
		return res, err
	}
	if _, err := srv.CreateUser(ctx, first, domainID, pass); err != nil {
		return res, fmt.Errorf("creating %s@%s: %w", first, o.Domain, err)
	}
	res.FirstUser, res.FirstPass = first+"@"+o.Domain, pass
	if err := writeCredentials(dir, res); err != nil {
		return res, err
	}

	zone, err := srv.DNSZone(ctx, domainID)
	if err == nil && zone != "" {
		res.ZoneFile = filepath.Join(dir, "dns.zone")
		if err := os.WriteFile(res.ZoneFile, []byte(zone), 0o644); err != nil {
			return res, err
		}
		log.Info("the records this domain needs are in %s", res.ZoneFile)
	}
	return res, nil
}

// Verify is the last step: does what was installed actually answer?
func Verify(ctx context.Context, res *Result, stackConsole, stackWebmail bool, log Log) []string {
	var problems []string
	check := func(what, url string, want string) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s did not answer at %s: %v", what, url, err))
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if want != "" && !strings.Contains(string(body), want) {
			problems = append(problems, fmt.Sprintf("%s answered at %s but did not look like itself", what, url))
			return
		}
		log.Info("%s answers", what)
	}
	check("the mail server", "http://127.0.0.1:8081/.well-known/jmap", "")
	if stackConsole {
		check("the console", "http://127.0.0.1:8082/", "api-base-url")
	}
	if stackWebmail {
		check("the webmail", "http://127.0.0.1:8080/api/health", "\"ok\":true")
	}
	return problems
}

func writeCredentials(dir string, r *Result) error {
	var b strings.Builder
	b.WriteString("# inbuxa -- written by the installer. Keep this file.\n")
	b.WriteString("# The administrator's password is not recoverable: nothing else holds it.\n\n")
	fmt.Fprintf(&b, "administrator   %s\npassword        %s\n", r.AdminUser, r.AdminPass)
	if r.FirstUser != "" {
		fmt.Fprintf(&b, "\nfirst mailbox   %s\npassword        %s\n", r.FirstUser, r.FirstPass)
	}
	if r.ConsoleURL != "" {
		fmt.Fprintf(&b, "\nconsole         %s\n", r.ConsoleURL)
	}
	if r.WebmailURL != "" {
		fmt.Fprintf(&b, "webmail         %s\n", r.WebmailURL)
	}
	return os.WriteFile(filepath.Join(dir, "credentials.txt"), []byte(b.String()), 0o600)
}

func live(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/jmap", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	// Unauthenticated, so 401 is the healthy answer: something is listening
	// and it is a JMAP server rather than a proxy error page.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("answered %s", resp.Status)
}

func waitFor(ctx context.Context, log Log, what string, limit time.Duration, probe func(context.Context) error) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		if err := probe(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("waited %s for %s: %w", limit, what, last)
}

func withLogs(ctx context.Context, err error, c docker.Compose, service string) error {
	logs := c.Logs(ctx, service, 25)
	if strings.TrimSpace(logs) == "" {
		return err
	}
	return fmt.Errorf("%w\n\nlast lines from %s:\n%s", err, service, logs)
}

func password(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[x.Int64()]
	}
	return string(b), nil
}
