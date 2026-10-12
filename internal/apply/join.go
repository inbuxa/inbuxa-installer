// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Join installs the console and the webmail on this machine, for a mail server
// that already runs on another one.
//
// The other machine is never reached except as a browser or a mail client
// would reach it: over its own HTTPS, with an API key its administrator made
// in the console. Through that key the front ends' addresses go into the
// server's inbuxa:FrontEnds, which registers their OAuth clients and puts
// their origins on the cross-origin allowlist, on every node, with no
// restart. Nothing is copied between the machines but that key, and the
// webmail's client secret, which is made here and leaves only inside that
// one request.

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// JoinCheck is what the server says before anything is installed: that the
// key works, that the server can take front ends this way, and that nothing
// it already lists would be replaced unasked. It changes nothing, so a dry
// run makes it too.
func JoinCheck(ctx context.Context, o plan.Options, key string) error {
	srv := &jmap.Client{BaseURL: o.ServerURL, Token: key}
	accountID, err := srv.AccountID(ctx)
	if err != nil {
		var he *jmap.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusUnauthorized {
			return fmt.Errorf("the server refused the API key; make one in the console under your account's API keys, then pass it with --key")
		}
		return fmt.Errorf("reaching %s: %w", o.ServerURL, err)
	}
	fe, err := srv.GetFrontEnds(ctx, accountID)
	if err != nil {
		return err
	}
	consoleURL, webmailURL := joinURLs(o)
	if o.Shape(plan.Console) != plan.Skip && fe.AdminURL != nil && *fe.AdminURL != consoleURL && !o.Replace {
		return fmt.Errorf("the server already lists a console at %s; pass --replace to put this one (%s) in its place", *fe.AdminURL, consoleURL)
	}
	if o.Shape(plan.Webmail) != plan.Skip {
		if fe.EnvironmentWebmailClientSecret {
			return fmt.Errorf("the server takes its webmail's client secret from INBUXA_WEBMAIL_CLIENT_SECRET in its environment, " +
				"so a webmail elsewhere can't be given one; remove that variable on the server, or join with --webmail skip")
		}
		if fe.WebmailURL != nil && *fe.WebmailURL != webmailURL && !o.Replace {
			return fmt.Errorf("the server already lists a webmail at %s; pass --replace to put this one (%s) in its place", *fe.WebmailURL, webmailURL)
		}
	}
	return nil
}

// joinURLs are the addresses the front ends here will be reached at.
func joinURLs(o plan.Options) (console, webmail string) {
	if o.Local {
		return "http://127.0.0.1:8082", "http://127.0.0.1:8080"
	}
	return "https://" + o.ConsoleHost, "https://" + o.WebmailHost
}

// Join carries out a join plan.
func Join(ctx context.Context, p plan.Plan, f host.Facts, key string, log Log) (*Result, error) {
	o := p.Options
	if o.ServerURL == "" {
		return nil, errors.New("join needs the server's address")
	}
	if o.Shape(plan.Server) != plan.Skip {
		return nil, errors.New("join installs front ends for a server elsewhere; the server itself is installed with install")
	}
	if o.Shape(plan.Console) == plan.Skip && o.Shape(plan.Webmail) == plan.Skip {
		return nil, errors.New("nothing to join: both the console and the webmail are skipped")
	}
	if st, err := state.Load(); err == nil && st.Shapes["server"] != "" {
		return nil, fmt.Errorf("this machine runs the mail server itself (a %s install); its front ends are added with install, not join", st.Shapes["server"])
	}

	log.Step("asking the server at %s", o.ServerURL)
	if err := JoinCheck(ctx, o, key); err != nil {
		return nil, err
	}
	srv := &jmap.Client{BaseURL: o.ServerURL, Token: key}
	accountID, err := srv.AccountID(ctx)
	if err != nil {
		return nil, err
	}
	log.Info("the key works, and the server takes front ends over JMAP")

	if len(p.Needs) > 0 && o.InstallDeps {
		log.Step("installing what this machine is missing")
		if err := deps.Resolve(ctx, log.Out(), p.Needs); err != nil {
			return nil, err
		}
		f = host.Survey(ctx)
	}
	// Containers only for what runs in one: a front end in a container, or
	// the managed Caddy. The console as files and the webmail under systemd,
	// behind your own web server, need none.
	needContainers := o.Shape(plan.Console) == plan.Container || o.Shape(plan.Webmail) == plan.Container ||
		(!o.Local && o.Proxy == "caddy")
	if needContainers && !f.Runtime.Usable {
		return nil, fmt.Errorf("what runs in containers here can't: %s", f.Runtime.Why)
	}

	consoleURL, webmailURL := joinURLs(o)
	stack := compose.Stack{
		Version:         "dev",
		Project:         "inbuxa",
		Domain:          o.Domain,
		Email:           o.ACMEEmail,
		Local:           o.Local,
		ConsoleHost:     o.ConsoleHost,
		WebmailHost:     o.WebmailHost,
		Console:         o.Shape(plan.Console) == plan.Container,
		Webmail:         o.Shape(plan.Webmail) == plan.Container,
		WebmailUnit:     o.Shape(plan.Webmail) == plan.Host,
		Proxy:           !o.Local && o.Proxy == "caddy",
		RemoteServerURL: o.ServerURL,
		ServerPublicURL: o.ServerURL,
		ConsoleImage:    DefaultConsoleImage,
		WebmailImage:    DefaultWebmailImage,
		CaddyImage:      DefaultCaddyImage,
		ConsoleBind:     "127.0.0.1:8082",
		WebmailBind:     "127.0.0.1:8080",
		Subnet:          DefaultSubnet,
		ACMEDirectory:   o.ACMEDirectory,
	}
	if o.Shape(plan.Console) != plan.Skip {
		stack.ConsoleURL = consoleURL
	}
	if o.Shape(plan.Console) == plan.Host {
		stack.ConsoleFiles = hostConsole{deploy: o.Dir}.dir()
	}
	if o.Shape(plan.Webmail) != plan.Skip {
		stack.WebmailURL = webmailURL
	}
	if o.ACMECARoot != "" {
		if err := copyACMERoot(o.ACMECARoot, o.Dir); err != nil {
			return nil, err
		}
		stack.ACMECARoot = "acme-ca-root.pem"
	}

	log.Step("writing the deployment into %s", o.Dir)
	var sec compose.Secrets
	if stack.Empty() {
		if sec, err = compose.EnsureSecrets(o.Dir); err != nil {
			return nil, err
		}
		log.Info(".env (nothing runs in containers here)")
	} else {
		if sec, err = compose.Write(o.Dir, &stack); err != nil {
			return nil, err
		}
		log.Info("compose.yaml, .env%s", map[bool]string{true: " and Caddyfile", false: ""}[stack.Proxy])
	}

	docker.UseRuntime(f.Runtime)
	cmp := docker.Compose{Dir: o.Dir, Runtime: f.Runtime}
	if !stack.Empty() {
		log.Step("fetching the images")
		if err := cmp.Run(ctx, "pull", "--quiet"); err != nil {
			return nil, err
		}
	}

	// On the host: the console's files, pointed at the server, and the
	// webmail's, with its unit and an environment naming the server. Both
	// in place before the server is told about them, so nothing it lists
	// is missing here.
	if o.Shape(plan.Console) == plan.Host {
		if err := (hostConsole{deploy: o.Dir, log: log}).Install(ctx, o.ServerURL); err != nil {
			return nil, err
		}
	}
	var hw *hostWebmail
	if o.Shape(plan.Webmail) == plan.Host {
		hw = &hostWebmail{deploy: o.Dir, log: log}
		if err := hw.Install(ctx); err != nil {
			return nil, err
		}
		if _, err := hw.WriteEnv(webmailEnv(o, stack, sec, hw.Version())); err != nil {
			return nil, err
		}
	}
	var proxyConfig string
	if !o.Local && o.Proxy == "snippets" {
		log.Step("writing the configuration for your web server")
		if proxyConfig, err = compose.WriteProxySnippets(o.Dir, stack); err != nil {
			return nil, fmt.Errorf("writing the proxy configuration: %w", err)
		}
		log.Info("%s/nginx-inbuxa.conf and %s/Caddyfile.fragment: load one into your web server", proxyConfig, proxyConfig)
	}

	log.Step("registering these front ends with the server")
	update := map[string]any{}
	if stack.ConsoleURL != "" {
		update["adminUrl"] = consoleURL
	}
	if stack.WebmailURL != "" {
		update["webmailUrl"] = webmailURL
		update["webmailClientSecret"] = sec.WebmailOAuth
	}
	if err := srv.SetFrontEnds(ctx, accountID, update); err != nil {
		return nil, fmt.Errorf("inbuxa:FrontEnds: %w", err)
	}
	for k, v := range update {
		if k != "webmailClientSecret" {
			log.Info("%s %v", k, v)
		}
	}

	// Every webmail request reaches the server from one address, this
	// machine's, and the server bans per address: exempted, a scanner
	// probing the webmail can't take everybody's webmail with it.
	if stack.WebmailURL != "" {
		addresses := joinExemptAddresses(ctx, o, stack)
		if len(addresses) == 0 {
			log.Info("could not tell which address the server sees the webmail at; exempt it with --exempt-address, or in the console under the auto-ban's allowed addresses")
		}
		for _, addr := range addresses {
			err := srv.AllowIP(ctx, addr, "the webmail at "+webmailURL+": every request arrives from this address")
			switch {
			case err == nil:
				log.Info("exempted %s from the auto-ban; the server applies it at its next restart", addr)
			case strings.Contains(err.Error(), "primaryKeyViolation"):
				// Exempted already: by an earlier join, or by hand
				log.Info("%s is already exempted from the auto-ban", addr)
			default:
				return nil, fmt.Errorf("exempting %s from the auto-ban: %w", addr, err)
			}
		}
	}

	log.Step("starting the front ends")
	if !stack.Empty() {
		if err := cmp.Run(ctx, "up", "-d", "--remove-orphans"); err != nil {
			return nil, err
		}
	}
	if hw != nil {
		if err := hw.Start(ctx); err != nil {
			return nil, withLogs(ctx, err, hw, "webmail")
		}
	}

	st, err := state.Load()
	if err == nil {
		st.ServerURL = o.ServerURL
		saveState(st, o, f, log)
	}

	return &Result{
		Dir:         o.Dir,
		ConsoleURL:  stack.ConsoleURL,
		WebmailURL:  stack.WebmailURL,
		ServerURL:   o.ServerURL,
		ProxyConfig: proxyConfig,
	}, nil
}

// joinExemptAddresses is where the server sees the webmail's requests come
// from: what the operator said, loopback for a server on this machine's own
// loopback, or else what the webmail's name resolves to -- the address its
// DNS points at this machine, which is the one it sends from unless
// something in between rewrites it.
func joinExemptAddresses(ctx context.Context, o plan.Options, stack compose.Stack) []string {
	if o.ExemptAddress != "" {
		return []string{o.ExemptAddress}
	}
	if stack.WebmailOnHost() {
		return []string{"127.0.0.1"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, o.WebmailHost)
	if err != nil {
		return nil
	}
	return addrs
}

// JoinVerify is join's last step: the front ends answer here, and the server
// lets the console call it from the browser, which is the part only the
// server can say.
func JoinVerify(ctx context.Context, res *Result, console, webmail plan.Shape, log Log) []string {
	// Started a moment ago: give them the time to answer at all before
	// asking whether they answer correctly.
	for _, c := range []struct {
		want bool
		what string
		url  string
	}{{console == plan.Container, "the console", "http://127.0.0.1:8082/"}, {webmail != plan.Skip, "the webmail", "http://127.0.0.1:8080/api/health"}} {
		if !c.want {
			continue
		}
		_ = waitFor(ctx, log, c.what+" to start", 60*time.Second, func(ctx context.Context) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				return fmt.Errorf("answered %s", resp.Status)
			}
			return nil
		})
	}
	o := plan.Options{Dir: res.Dir, Choices: []plan.Choice{
		{Component: plan.Console, Shape: console},
		{Component: plan.Webmail, Shape: webmail},
	}}
	problems := Verify(ctx, res, o, false, log)
	if console != plan.Skip {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodOptions, res.ServerURL+"/jmap/session", nil)
		origin := strings.TrimRight(res.ConsoleURL, "/")
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		resp, err := (&http.Client{}).Do(req)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("the server did not answer at %s: %v", res.ServerURL, err))
		case resp.Header.Get("Access-Control-Allow-Origin") != origin:
			resp.Body.Close()
			problems = append(problems, fmt.Sprintf("the server does not let the console at %s call it yet (no Access-Control-Allow-Origin)", origin))
		default:
			resp.Body.Close()
			log.Info("the server lets the console call it")
		}
	}
	return problems
}

// copyACMERoot puts a private ACME CA's root beside the deployment, for
// Caddy: the only thing here that asks for certificates.
func copyACMERoot(path, dir string) error {
	root, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading the ACME CA root: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "acme-ca-root.pem"), root, 0o644)
}
