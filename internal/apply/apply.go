// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
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
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Images are the defaults. They are tags rather than digests for now: the
// registry is ours and the tags are immutable releases, and pinning digests
// here would mean this program needing a release of its own for every one of
// theirs.
const (
	DefaultServerImage  = "registry.coffeylabs.org/inbuxa/inbuxa-server:2026.9.30.2"
	DefaultConsoleImage = "registry.coffeylabs.org/inbuxa/inbuxa-admin:2026.9.30"
	DefaultWebmailImage = "registry.coffeylabs.org/inbuxa/inbuxa-webmail:2026.10.5-g17a8093"
	DefaultCaddyImage   = "docker.io/library/caddy:2-alpine"
	DefaultSubnet       = "172.31.253.0/24"
)

// Result is what the operator is left holding.
type Result struct {
	Dir         string
	AdminUser   string
	AdminPass   string
	FirstUser   string
	FirstPass   string
	ZoneFile    string
	Certificate string // what issued the mail server's certificate, when one arrived
	ConsoleURL  string
	WebmailURL  string
	ServerURL   string
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
func Run(ctx context.Context, p plan.Plan, f host.Facts, log Log) (*Result, error) {
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
		// The survey was taken before any of that existed. Installing podman
		// on a machine that had no runtime and then driving the deployment
		// with the old facts meant reaching for a docker that was never
		// going to be there.
		f = host.Survey(ctx)
		if !f.Runtime.Usable {
			return nil, fmt.Errorf("after installing what was missing, containers still are not usable: %s", f.Runtime.Why)
		}
		log.Info("using %s %s", f.Runtime.Kind, f.Runtime.Version)
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

	// A private ACME CA has to be trusted by two programs that never see each
	// other's trust store: the mail server, which asks for its own
	// certificate over HTTP-01, and Caddy, which asks for the front ends'.
	// The server gets the image's own roots plus this one as a file it mounts
	// over its bundle; Caddy gets the root on its own.
	if o.ACMECARoot != "" {
		root, err := os.ReadFile(o.ACMECARoot)
		if err != nil {
			return nil, fmt.Errorf("reading the ACME CA root: %w", err)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		bundle, err := docker.SystemCABundle(ctx, stack.ServerImage)
		if err != nil {
			return nil, fmt.Errorf("reading the server image's CA bundle: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ca-bundle.crt"), append(bundle, root...), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "acme-ca-root.pem"), root, 0o644); err != nil {
			return nil, err
		}
		stack.CABundle = true
		stack.ACMECARoot = "acme-ca-root.pem"
	}
	stack.ACMEDirectory = o.ACMEDirectory

	log.Step("writing the deployment into %s", dir)
	if _, err := compose.Write(dir, &stack); err != nil {
		return nil, err
	}
	log.Info("compose.yaml, .env%s", map[bool]string{true: " and Caddyfile", false: ""}[stack.Proxy])

	// Whichever runtime the survey found: docker where there is one, podman
	// on the Red Hat family, driven through the same compose plugin.
	docker.UseRuntime(f.Runtime)
	cmp := docker.Compose{Dir: dir, Runtime: f.Runtime}

	log.Step("fetching the images")
	if err := cmp.Run(ctx, "pull", "--quiet"); err != nil {
		return nil, err
	}

	// A machine that already runs this installation is converged, not set up
	// again. First boot happens once: bootstrap, the first administrator, the
	// first account, the ACME account. Running it a second time asks a
	// configured server for bootstrap credentials it stopped accepting the
	// moment it was configured -- which is exactly what adding or removing a
	// front end used to do, and why it could not.
	if existing, err := state.Load(); err == nil && existing.Installed() {
		return converge(ctx, o, stack, cmp, existing, f, log)
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

	// Once configured, the server takes a token, never a password, on /jmap
	// (contract C-23), and the bootstrap credential no longer works; nor
	// does anything but x:Bootstrap work before then. So the server comes up
	// configured with Basic allowed, for this one step only, long enough for
	// the administrator to give itself an API key with its password. The key
	// does the rest of the setup, is removed when the setup ends, and expires
	// within the hour anyway, so a setup that stops halfway leaves nothing
	// that still works.
	basicOverride := filepath.Join(os.TempDir(), "inbuxa-basic-auth.yaml")
	if err := os.WriteFile(basicOverride, []byte(
		"services:\n  server:\n    environment:\n      INBUXA_HTTP_BASIC_AUTH: all\n"), 0o600); err != nil {
		return res, err
	}
	defer os.Remove(basicOverride)
	log.Step("restarting the mail server configured, to give the setup an API key")
	basic := cmp
	basic.Files = []string{basicOverride}
	if err := basic.Run(ctx, "up", "-d", "server"); err != nil {
		return res, err
	}
	adminBasic := &jmap.Client{BaseURL: serverURL, Username: admin.Username, Password: admin.Secret}
	var keyID, key string
	if err := waitFor(ctx, log, "the server to come back configured", 120*time.Second, func(ctx context.Context) error {
		var err error
		keyID, key, err = adminBasic.CreateAPIKey(ctx, "inbuxa installer, for this setup only", time.Hour)
		return err
	}); err != nil {
		return res, withLogs(ctx, fmt.Errorf("an API key for the setup: %w", err), cmp, "server")
	}
	srv := &jmap.Client{BaseURL: serverURL, Token: key}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := srv.DestroyAPIKey(ctx, keyID); err != nil {
			log.Info("could not remove the setup's API key, which expires within the hour: %v", err)
		}
	}()

	log.Step("starting the rest of the stack")
	// Without the override, so the server is recreated taking tokens only.
	// --remove-orphans: the compose file is rendered from the shapes asked
	// for, so a component the topology no longer lists is simply not in it
	// any more. Without this its container would keep running, belonging to
	// a project that no longer describes it.
	if err := cmp.Run(ctx, "up", "-d", "--remove-orphans"); err != nil {
		return res, err
	}

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

	// Certificates need something holding port 80 for the HTTP-01 challenge,
	// which is the proxy. Asked for on a machine with no proxy, the order
	// can only fail -- so this says whose job it is instead of leaving a
	// failed ACME account behind and stopping the install over it.
	if !o.Local && !stack.Proxy {
		log.Info("no proxy here, so certificates are yours to arrange: the server's own names are %s",
			strings.Join(stack.ServerNames(), ", "))
	}
	if !o.Local && stack.Proxy {
		log.Step("turning on certificates")
		if _, err := srv.EnableACME(ctx, domainID, o.ACMEDirectory, o.ACMEEmail); err != nil {
			return res, fmt.Errorf("enabling ACME: %w", err)
		}
		// An order that fails is not retried on its own, and a restart does
		// not start a new one: moving the domain to manual and back is what
		// does. So this waits, and asks again every so often, rather than
		// declaring the install finished over a self-signed certificate that
		// every mail client will refuse.
		if issuer := waitForCertificate(ctx, srv, log, domainID, o.MailHost, 3*time.Minute); issuer != "" {
			res.Certificate = issuer
			log.Info("certificate for %s issued by %s", o.MailHost, issuer)
		} else {
			log.Info("no certificate yet for %s: the server keeps trying, and will succeed once %s resolves to this machine and port 80 reaches it",
				o.MailHost, o.MailHost)
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

	// What this machine now runs, written down so the next run diffs against
	// intent rather than guessing from what happens to be up.
	st, err := state.Load()
	if err == nil {
		st.Machine, st.Dir, st.Domain = o.Machine, dir, o.Domain
		st.Runtime, st.Topology = f.Runtime.Kind, o.TopologyPath
		st.Shapes = map[string]string{}
		for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
			if sh := o.Shape(c); sh != plan.Skip {
				st.Shapes[string(c)] = string(sh)
			}
		}
		if err := st.Save(); err != nil {
			log.Info("could not write %s: %v", state.Path, err)
		}
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

// waitForCertificate waits for the mail server's own certificate to arrive,
// asking for a fresh order every 45 seconds. It returns the issuer, or ""
// when none arrived in time -- which is not a failure: on a real install the
// domain often does not point here yet, and the server goes on trying.
func waitForCertificate(ctx context.Context, srv *jmap.Client, log Log, domainID, host string, limit time.Duration) string {
	deadline := time.Now().Add(limit)
	nextRetry := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		certs, err := srv.Certificates(ctx)
		if err == nil {
			for _, c := range certs {
				if c.SubjectAlternativeNames[host] && !strings.Contains(c.Issuer, "self signed") {
					return c.Issuer
				}
			}
		}
		if time.Now().After(nextRetry) {
			log.Info("asking for the certificate again")
			_ = srv.RetryCertificates(ctx, domainID)
			nextRetry = time.Now().Add(45 * time.Second)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(3 * time.Second):
		}
	}
	return ""
}

// converge brings an installed machine in line with what it is now asked to
// run: the deployment is rewritten from the shapes chosen, the stack is
// brought up, and anything no longer in the file goes with --remove-orphans.
// Nothing touches the mail: data volumes are left alone, and a component
// that is removed can be added back with what it had.
func converge(ctx context.Context, o plan.Options, stack compose.Stack, cmp docker.Compose,
	st *state.State, f host.Facts, log Log) (*Result, error) {

	res := &Result{
		Dir: o.Dir, ConsoleURL: stack.ConsoleURL, WebmailURL: stack.WebmailURL,
		ServerURL: stack.ServerPublicURL,
	}
	if b, err := os.ReadFile(filepath.Join(o.Dir, "credentials.txt")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "administrator" {
				res.AdminUser = fields[1]
			}
		}
	}

	// Before anything is recreated: an install from before #5 keeps the
	// server's data where the corrected compose file no longer mounts it.
	moved, err := adoptServerVolumes(ctx, cmp, stack.Project, log)
	if err != nil {
		return res, err
	}

	log.Step("bringing the stack in line with the file")
	if err := cmp.Run(ctx, "up", "-d", "--remove-orphans"); err != nil {
		return res, withLogs(ctx, err, cmp, "server")
	}
	if moved {
		// A server that finds no configuration starts in bootstrap mode and
		// says so. That would mean the move missed something; the old
		// volumes are still there to put back.
		if err := waitFor(ctx, log, "the server to come back on its named volumes", 120*time.Second, func(ctx context.Context) error {
			return live(ctx, "http://"+stack.ServerBind)
		}); err != nil {
			return res, withLogs(ctx, err, cmp, "server")
		}
		if strings.Contains(cmp.Logs(ctx, "server", 400), "bootstrap mode") {
			return res, fmt.Errorf("the server came back in bootstrap mode after its data was moved; " +
				"its previous volumes are untouched, see `docker volume ls` and issue #5")
		}
		log.Info("the server came back with its configuration")
	}

	// The server reads the front-end URLs from its environment, so a front
	// end that was added or removed changes what it was started with. Only
	// restart when that actually changed.
	if st.Shapes["console"] != string(o.Shape(plan.Console)) || st.Shapes["webmail"] != string(o.Shape(plan.Webmail)) {
		log.Info("the front ends changed, so the server is restarted to see them")
		if err := cmp.Run(ctx, "restart", "server"); err != nil {
			return res, err
		}
		// Do not report a finished converge over a server that is still
		// coming back: a few seconds of refused connections is the one
		// moment of this that looks like an outage, and it should be over
		// before the command returns.
		if err := waitFor(ctx, log, "the server to answer again", 120*time.Second, func(ctx context.Context) error {
			return live(ctx, "http://"+stack.ServerBind)
		}); err != nil {
			return res, withLogs(ctx, err, cmp, "server")
		}
	}

	st.Machine, st.Dir, st.Domain = o.Machine, o.Dir, o.Domain
	st.Runtime, st.Topology = f.Runtime.Kind, o.TopologyPath
	st.Shapes = map[string]string{}
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		if sh := o.Shape(c); sh != plan.Skip {
			st.Shapes[string(c)] = string(sh)
		}
	}
	if err := st.Save(); err != nil {
		log.Info("could not write %s: %v", state.Path, err)
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
