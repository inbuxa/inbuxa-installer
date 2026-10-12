// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// The console and the webmail on the host, beside a server in a container,
// on the host, or (later) elsewhere.
//
// The console is static files: unpacked into <dir>/console, given the
// server's address the way its image's entrypoint does, and served by the
// managed Caddy or by the operator's web server from the configuration
// written for it. Nothing runs.
//
// The webmail is the /app its image runs, on the installer's own Node, under
// a unit of its own, as a user of its own that can't write a byte of its own
// files -- which is what its IMMUTABLE setting checks for, as a read-only
// container root would give it.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/compose"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
)

const (
	HostWebmailUnit = "inbuxa-webmail.service"
	HostWebmailUser = "inbuxa-webmail"

	webmailUnitPath = "/etc/systemd/system/" + HostWebmailUnit
	releaseMarker   = ".inbuxa-release" // what was unpacked, so a converge can skip it
)

// fetchRelease puts a release tarball at a temporary path: the file named by
// the override variable when it is set (a build to test, said loudly), or
// the release download, checked against its pinned checksum.
func fetchRelease(ctx context.Context, log Log, what, override, url, sum string) (path, marker string, cleanup func(), err error) {
	if p := os.Getenv(override); p != "" {
		log.Info("%s from %s instead of the release, as %s says (no checksum)", what, p, override)
		return p, "local:" + p, func() {}, nil
	}
	if sum == "" {
		return "", "", nil, fmt.Errorf("there is no %s release with files to install for %s", what, runtime.GOARCH)
	}
	work, err := os.MkdirTemp("", "inbuxa-"+what+"-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(work) }
	path = filepath.Join(work, what+".tar.gz")
	if err := deps.FetchVerified(ctx, log.Out(), url, sum, path, 0o644); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return path, sum, cleanup, nil
}

// unpack replaces dest with the tarball's contents, owned by root and
// readable by everyone, and only once they are all there: a failure part way
// leaves the previous version in place.
func unpack(ctx context.Context, tarball, dest, marker string) error {
	next, old := dest+".new", dest+".old"
	os.RemoveAll(next)
	os.RemoveAll(old)
	if err := os.MkdirAll(next, 0o755); err != nil {
		return err
	}
	if err := runQuiet(ctx, "tar", "-xzf", tarball, "-C", next, "--no-same-owner"); err != nil {
		os.RemoveAll(next)
		return fmt.Errorf("unpacking %s: %w", filepath.Base(tarball), err)
	}
	if err := runQuiet(ctx, "chmod", "-R", "u=rwX,go=rX", next); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(next, releaseMarker), []byte(marker+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(next, dest); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

// markerFor is what the marker records: the checksum and its release, or
// "local" for a tarball given by hand, which upgrade then always replaces.
func markerFor(sum, version string) string {
	if strings.HasPrefix(sum, "local:") {
		return sum + " local"
	}
	return sum + " " + version
}

func unpacked(dest, marker string) bool {
	return markerSum(dest) == marker && !strings.HasPrefix(marker, "local:")
}

// The marker is the tarball's checksum and, after it, the release it came
// from: the checksum decides whether to unpack again, the version is what
// status and upgrade show. An install from before the version was written
// has the checksum alone.
func markerSum(dest string) string {
	b, err := os.ReadFile(filepath.Join(dest, releaseMarker))
	if err != nil {
		return ""
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// ---- the console ----

type hostConsole struct {
	deploy string
	log    Log
}

func (c hostConsole) dir() string { return filepath.Join(c.deploy, "console") }

var apiBaseMeta = regexp.MustCompile(`<meta name="api-base-url"[^>]*>`)

// Install puts the console's files in place and points them at the server.
func (c hostConsole) Install(ctx context.Context, serverURL string) error {
	marker := release.ConsoleSHA256
	if os.Getenv("INBUXA_CONSOLE_TARBALL") != "" {
		marker = "local:" + os.Getenv("INBUXA_CONSOLE_TARBALL")
	}
	if !unpacked(c.dir(), marker) {
		c.log.Step("installing the console's files")
		path, mark, cleanup, err := fetchRelease(ctx, c.log, "console", "INBUXA_CONSOLE_TARBALL", release.ConsoleTarball(), release.ConsoleSHA256)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := os.MkdirAll(c.deploy, 0o750); err != nil {
			return err
		}
		if err := unpack(ctx, path, c.dir(), markerFor(mark, release.ConsoleVersion)); err != nil {
			return err
		}
	}
	if err := setAPIBase(filepath.Join(c.dir(), "index.html"), serverURL); err != nil {
		return err
	}
	// The web server reads them, whoever it runs as.
	if err := os.Chmod(c.deploy, 0o711); err != nil {
		return err
	}
	c.log.Info("%s, pointed at %s", c.dir(), serverURL)
	return nil
}

// setAPIBase does what the console image's entrypoint does at start: names
// the server the page talks to, in a meta tag, replacing one that is there.
func setAPIBase(index, url string) error {
	b, err := os.ReadFile(index)
	if err != nil {
		return fmt.Errorf("the console's index.html: %w", err)
	}
	tag := `<meta name="api-base-url" content="` + strings.ReplaceAll(url, `"`, "&quot;") + `">`
	html := string(b)
	if apiBaseMeta.MatchString(html) {
		html = apiBaseMeta.ReplaceAllLiteralString(html, tag)
	} else {
		html = strings.Replace(html, "<head>", "<head>"+tag, 1)
	}
	return os.WriteFile(index, []byte(html), 0o644)
}

// ---- the webmail ----

type hostWebmail struct {
	deploy string
	log    Log
}

func (w hostWebmail) dir() string     { return filepath.Join(w.deploy, "webmail") }
func (w hostWebmail) envPath() string { return filepath.Join(w.deploy, "webmail.env") }

// Install puts the webmail's files, its user and its unit in place. It does
// not start it: it can only sign anyone in once the server is set up.
func (w hostWebmail) Install(ctx context.Context) error {
	arch := runtime.GOARCH
	marker := release.WebmailSHA256[arch]
	if os.Getenv("INBUXA_WEBMAIL_TARBALL") != "" {
		marker = "local:" + os.Getenv("INBUXA_WEBMAIL_TARBALL")
	}
	if !unpacked(w.dir(), marker) {
		w.log.Step("installing the webmail on this machine")
		path, mark, cleanup, err := fetchRelease(ctx, w.log, "webmail", "INBUXA_WEBMAIL_TARBALL", release.WebmailTarball(arch), release.WebmailSHA256[arch])
		if err != nil {
			return err
		}
		defer cleanup()
		if err := os.MkdirAll(w.deploy, 0o750); err != nil {
			return err
		}
		if err := unpack(ctx, path, w.dir(), markerFor(mark, release.WebmailVersion)); err != nil {
			return err
		}
		w.log.Info("%s (owned by root: the webmail reads it and can't change it)", w.dir())
	}
	if err := exec.CommandContext(ctx, "getent", "passwd", HostWebmailUser).Run(); err != nil {
		if err := runQuiet(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", w.dir(), "--shell", nologin(), HostWebmailUser); err != nil {
			return fmt.Errorf("creating the %s user: %w", HostWebmailUser, err)
		}
		w.log.Info("user %s (system, no login)", HostWebmailUser)
	}
	if err := os.Chmod(w.deploy, 0o711); err != nil {
		return err
	}
	return w.writeUnit(ctx)
}

// Version is what the unpacked webmail says it is, which it reports as
// IHASMAIL_VERSION, as its image does.
func (w hostWebmail) Version() string {
	b, _ := os.ReadFile(filepath.Join(w.dir(), "VERSION"))
	return strings.TrimSpace(string(b))
}

// WriteEnv writes the webmail's environment, the same as its container's,
// and says whether it changed. Empty values are written: SESSION_FILE= is how
// the webmail is told to keep sessions in memory only.
func (w hostWebmail) WriteEnv(vars map[string]string) (bool, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by the inbuxa installer: the webmail's environment.\n")
	b.WriteString("# Read by " + HostWebmailUnit + " at each start.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, vars[k])
	}
	return writeIfChanged(w.envPath(), b.String(), 0o600)
}

func (w hostWebmail) writeUnit(ctx context.Context) error {
	unit := fmt.Sprintf(`# Written by the inbuxa installer. Changes here are replaced on its next run.
[Unit]
Description=inbuxa webmail
Documentation=https://docs.inbuxa.org/
After=network-online.target inbuxa-server.service
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
EnvironmentFile=%s
ExecStart=%s %s/server/dist/index.js
WorkingDirectory=%s
# It writes nothing: sessions are in memory (SESSION_FILE= in its
# environment), and its own files are root's.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, HostWebmailUser, HostWebmailUser, w.envPath(), host.OwnPath, w.dir(), w.dir())
	changed, err := writeIfChanged(webmailUnitPath, unit, 0o644)
	if err != nil {
		return err
	}
	if changed {
		if err := runQuiet(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	return runQuiet(ctx, "systemctl", "enable", "--quiet", HostWebmailUnit)
}

// Start (re)starts the webmail and waits for it to say it is healthy.
func (w hostWebmail) Start(ctx context.Context) error {
	if err := runQuiet(ctx, "systemctl", "restart", HostWebmailUnit); err != nil {
		return err
	}
	return waitFor(ctx, w.log, "the webmail", 60*time.Second, func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/api/health", nil)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("answered %s", resp.Status)
		}
		return nil
	})
}

// Logs is the webmail's last lines from the journal.
func (w hostWebmail) Logs(ctx context.Context, _ string, lines int) string {
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", HostWebmailUnit,
		"-n", fmt.Sprint(lines), "--no-pager", "-o", "cat").CombinedOutput()
	return string(out)
}

// webmailEnv is the webmail's environment on the host: what its compose
// service is given, plus what its image bakes in (NODE_ENV, STATIC_DIR, its
// version). It listens on loopback only, where the proxy reaches it, and
// reaches a server on this machine at its loopback port whichever shape the
// server is.
func webmailEnv(o plan.Options, stack compose.Stack, sec compose.Secrets, version string) map[string]string {
	server := "http://127.0.0.1:8081"
	if stack.RemoteServerURL != "" {
		server = strings.TrimRight(stack.RemoteServerURL, "/")
	}
	env := map[string]string{
		"NODE_ENV":            "production",
		"HOST":                "127.0.0.1",
		"PORT":                "8080",
		"STATIC_DIR":          filepath.Join(o.Dir, "webmail", "web", "dist"),
		"IHASMAIL_VERSION":    version,
		"MAIL_SERVER_URL":     server,
		"APP_SECRET":          sec.AppSecret,
		"APP_NAME":            "inbuxa",
		"IMMUTABLE":           "1",
		"SESSION_FILE":        "",
		"TRUST_PROXY":         "1",
		"IMAGE_PROXY":         "1",
		"OAUTH_CLIENT_ID":     "ihasmail-inbuxa",
		"OAUTH_CLIENT_SECRET": sec.WebmailOAuth,
		"PUBLIC_URL":          stack.WebmailURL,
	}
	if stack.ConsoleURL != "" {
		env["ADMIN_URL"] = stack.ConsoleURL
	}
	if !o.Local {
		env["PUSH_URL"] = stack.WebmailURL
	}
	return env
}
