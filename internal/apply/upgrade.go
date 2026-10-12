// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Upgrade moves what is installed here to the releases this installer pins.
// A newer installer is what brings newer releases; upgrading is running it.
//
// One component at a time, server first (the front ends follow the server's
// API, not the other way round), each checked once it is running again and
// put back as it was if the check fails, which stops the upgrade there:
//
//   - a container gets its new image by its tag in the compose file the
//     installer wrote, and the old tag back to undo it;
//   - the server on the host keeps its old binary beside the new one, as
//     /usr/local/bin/inbuxa-server.<version>;
//   - the console and the webmail on the host keep their old directory
//     until the new one answers.
//
// Before the server, which may convert its store on first start and can't
// convert it back, it is stopped and its own data archived into
// <dir>/backups, unless the operator says not to.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Step is one component's move: what it is now, and what it would be.
type Step struct {
	Component plan.Component
	Shape     plan.Shape
	From, To  string
}

// Due is whether there is anything to do.
func (s Step) Due() bool { return s.From != s.To }

// repos are the image repositories in the compose file, by component.
var repos = map[plan.Component]string{plan.Server: "inbuxa-server", plan.Console: "inbuxa-admin", plan.Webmail: "inbuxa-webmail"}

func tagOf(image string) string { return image[strings.LastIndex(image, ":")+1:] }

// targets are what this installer installs, by component and shape.
func target(c plan.Component, s plan.Shape) string {
	switch {
	case c == plan.Server && s == plan.Container:
		return tagOf(DefaultServerImage)
	case c == plan.Console && s == plan.Container:
		return tagOf(DefaultConsoleImage)
	case c == plan.Webmail && s == plan.Container:
		return tagOf(DefaultWebmailImage)
	case c == plan.Server:
		return release.ServerVersion
	case c == plan.Console:
		return release.ConsoleVersion
	default:
		return release.WebmailVersion
	}
}

// Plan reads what each installed component is, from the machine rather
// than from notes, and pairs it with what this installer would put there.
func UpgradePlan(st *state.State) []Step {
	var steps []Step
	compose, _ := os.ReadFile(filepath.Join(st.Dir, "compose.yaml"))
	for _, c := range []plan.Component{plan.Server, plan.Console, plan.Webmail} {
		shape := plan.Shape(st.Shapes[string(c)])
		if shape != plan.Container && shape != plan.Host {
			continue
		}
		steps = append(steps, Step{Component: c, Shape: shape, From: installedVersion(st.Dir, c, shape, string(compose)), To: target(c, shape)})
	}
	return steps
}

func installedVersion(dir string, c plan.Component, s plan.Shape, compose string) string {
	if s == plan.Container {
		m := regexp.MustCompile(`(?m)^\s+image:\s+\S+/` + repos[c] + `:(\S+)\s*$`).FindStringSubmatch(compose)
		if m == nil {
			return "unknown"
		}
		return m[1]
	}
	switch c {
	case plan.Server:
		if f := strings.Fields(binaryVersion(context.Background(), HostServerBinary)); len(f) > 0 {
			return f[0]
		}
	case plan.Console:
		d := filepath.Join(dir, "console")
		if markerSum(d) == release.ConsoleSHA256 {
			return release.ConsoleVersion
		}
		if b, err := os.ReadFile(filepath.Join(d, releaseMarker)); err == nil {
			if f := strings.Fields(string(b)); len(f) > 1 {
				return f[1]
			}
			return "an earlier release"
		}
	case plan.Webmail:
		// Its own VERSION says 2026.10.12+g6e1a1cb; its release tag, and
		// so the pin, the same with a hyphen.
		if b, err := os.ReadFile(filepath.Join(dir, "webmail", "VERSION")); err == nil {
			return strings.ReplaceAll(strings.TrimSpace(string(b)), "+", "-")
		}
	}
	return "unknown"
}

// RunUpgrade carries out the steps that are due, in order.
func RunUpgrade(ctx context.Context, st *state.State, steps []Step, backup bool, f host.Facts, log Log) error {
	dir := st.Dir
	docker.UseRuntime(f.Runtime)
	cmp := docker.Compose{Dir: dir, Runtime: f.Runtime}
	for _, s := range steps {
		if !s.Due() {
			continue
		}
		log.Step("%s: %s -> %s (%s)", s.Component, s.From, s.To, s.Shape)
		var err error
		switch {
		case s.Component == plan.Server && s.Shape == plan.Container:
			err = upgradeServerContainer(ctx, dir, s, cmp, backup, log)
		case s.Component == plan.Server:
			err = upgradeServerHost(ctx, dir, s, backup, log)
		case s.Shape == plan.Container:
			err = upgradeFrontEndContainer(ctx, dir, s, cmp, log)
		case s.Component == plan.Console:
			err = upgradeConsoleHost(ctx, dir, log)
		default:
			err = upgradeWebmailHost(ctx, dir, log)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.Component, err)
		}
	}
	st.Installer = "server " + release.ServerVersion
	if err := st.Save(); err != nil {
		log.Info("could not write %s: %v", state.Path, err)
	}
	return nil
}

// ---- containers ----

// setTag points a component's service at another tag of its image, and
// returns the tag it had.
func setTag(dir string, c plan.Component, tag string) (string, error) {
	path := filepath.Join(dir, "compose.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`(?m)^(\s+image:\s+\S+/` + repos[c] + `):(\S+)\s*$`)
	m := re.FindStringSubmatch(string(b))
	if m == nil {
		return "", fmt.Errorf("no %s image in %s", repos[c], path)
	}
	out := re.ReplaceAllString(string(b), "${1}:"+tag)
	return m[2], os.WriteFile(path, []byte(out), 0o640)
}

func service(c plan.Component) string { return string(c) }

func upgradeServerContainer(ctx context.Context, dir string, s Step, cmp docker.Compose, backup bool, log Log) error {
	if backup {
		log.Info("stopping the server to archive its data")
		if err := cmp.Run(ctx, "stop", "server"); err != nil {
			return err
		}
		name, err := archiveVolumes(ctx, dir, s.From)
		if err != nil {
			cmp.Run(ctx, "start", "server")
			return fmt.Errorf("archiving the server's data, so nothing was changed: %w", err)
		}
		log.Info("archived its configuration and data to %s", name)
		log.Info("a store this server keeps elsewhere (PostgreSQL, S3) is backed up where it lives, not here")
	}
	return swapContainer(ctx, dir, s, cmp, log, func(ctx context.Context) error {
		return live(ctx, "http://127.0.0.1:8081")
	})
}

// archiveVolumes tars the server's two named volumes, read-only, with the
// server's own (old) image, which has tar: nothing else is pulled.
func archiveVolumes(ctx context.Context, dir, from string) (string, error) {
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("server-%s-%s.tar.gz", from, time.Now().UTC().Format("20060102T150405Z"))
	image := strings.TrimSuffix(DefaultServerImage, ":"+tagOf(DefaultServerImage)) + ":" + from
	_, err := docker.Output(ctx, "run", "--rm", "--user", "0", "--entrypoint", "tar",
		"-v", "inbuxa_inbuxa-etc:/archive/etc:ro", "-v", "inbuxa_inbuxa-data:/archive/data:ro",
		"-v", backups+":/backups", image, "-czf", "/backups/"+name, "-C", "/archive", ".")
	return filepath.Join(backups, name), err
}

func upgradeFrontEndContainer(ctx context.Context, dir string, s Step, cmp docker.Compose, log Log) error {
	check := func(ctx context.Context) error { return answers(ctx, "http://127.0.0.1:8082/", "api-base-url") }
	if s.Component == plan.Webmail {
		check = func(ctx context.Context) error { return answers(ctx, "http://127.0.0.1:8080/api/health", `"ok":true`) }
	}
	return swapContainer(ctx, dir, s, cmp, log, check)
}

// swapContainer moves one service to its new tag, and back if it doesn't
// pass its check.
func swapContainer(ctx context.Context, dir string, s Step, cmp docker.Compose, log Log, check func(context.Context) error) error {
	old, err := setTag(dir, s.Component, s.To)
	if err != nil {
		return err
	}
	back := func(why error) error {
		log.Info("putting %s back to %s", s.Component, old)
		setTag(dir, s.Component, old)
		cmp.Run(ctx, "up", "-d", service(s.Component))
		return fmt.Errorf("%w; it was put back to %s", why, old)
	}
	if err := cmp.Run(ctx, "pull", "--quiet", service(s.Component)); err != nil {
		return back(err)
	}
	if err := cmp.Run(ctx, "up", "-d", service(s.Component)); err != nil {
		return back(err)
	}
	if err := waitFor(ctx, log, string(s.Component)+" "+s.To, 120*time.Second, check); err != nil {
		return back(withLogs(ctx, err, cmp, service(s.Component)))
	}
	log.Info("%s %s answers", s.Component, s.To)
	return nil
}

// ---- on the host ----

func upgradeServerHost(ctx context.Context, dir string, s Step, backup bool, log Log) error {
	hs := hostServer{deploy: dir, log: log}
	kept := HostServerBinary + "." + s.From
	if err := runQuiet(ctx, "cp", "-a", HostServerBinary, kept); err != nil {
		return err
	}
	log.Info("kept %s, to go back to", kept)
	if err := runQuiet(ctx, "systemctl", "stop", HostServerUnit); err != nil {
		return err
	}
	if backup {
		backups := filepath.Join(dir, "backups")
		if err := os.MkdirAll(backups, 0o700); err != nil {
			return err
		}
		name := filepath.Join(backups, fmt.Sprintf("server-%s-%s.tar.gz", s.From, time.Now().UTC().Format("20060102T150405Z")))
		if err := runQuiet(ctx, "tar", "-czf", name, "-C", dir, "server"); err != nil {
			runQuiet(ctx, "systemctl", "start", HostServerUnit)
			return fmt.Errorf("archiving the server's data, so nothing was changed: %w", err)
		}
		log.Info("archived its configuration and data to %s", name)
		log.Info("a store this server keeps elsewhere (PostgreSQL, S3) is backed up where it lives, not here")
	}
	back := func(why error) error {
		log.Info("putting the server back to %s", s.From)
		runQuiet(ctx, "install", "-m", "0755", kept, HostServerBinary)
		runQuiet(ctx, "systemctl", "restart", HostServerUnit)
		return fmt.Errorf("%w; it was put back to %s", why, s.From)
	}
	var err error
	if p := os.Getenv("INBUXA_SERVER_BINARY"); p != "" {
		log.Info("installing %s instead of the release, as INBUXA_SERVER_BINARY says (no checksum)", p)
		err = runQuiet(ctx, "install", "-m", "0755", p, HostServerBinary)
	} else {
		err = hs.fetchBinary(ctx)
	}
	if err != nil {
		return back(err)
	}
	if err := runQuiet(ctx, "systemctl", "start", HostServerUnit); err != nil {
		return back(err)
	}
	if err := waitFor(ctx, log, "the server "+s.To, 120*time.Second, func(ctx context.Context) error {
		return live(ctx, "http://"+hostFinalBind)
	}); err != nil {
		return back(withLogs(ctx, err, hs, "server"))
	}
	log.Info("the server %s answers", s.To)
	return nil
}

// replaceDir puts a new release in dest, keeping the old one as dest.prev
// until install says the new one works, and putting it back if not -- then
// undo, for whatever else install changed.
func replaceDir(dest string, install func() error, undo func(), log Log) error {
	prev := dest + ".prev"
	os.RemoveAll(prev)
	if err := os.Rename(dest, prev); err != nil {
		return err
	}
	if err := install(); err != nil {
		os.RemoveAll(dest)
		os.Rename(prev, dest)
		undo()
		log.Info("put the previous release back in %s", dest)
		return fmt.Errorf("%w; the previous release was put back", err)
	}
	return os.RemoveAll(prev)
}

func upgradeConsoleHost(ctx context.Context, dir string, log Log) error {
	c := hostConsole{deploy: dir, log: log}
	index := filepath.Join(c.dir(), "index.html")
	b, err := os.ReadFile(index)
	if err != nil {
		return err
	}
	m := regexp.MustCompile(`<meta name="api-base-url" content="([^"]*)"`).FindStringSubmatch(string(b))
	if m == nil || m[1] == "" {
		return fmt.Errorf("%s names no server to keep pointing at", index)
	}
	server := m[1]
	return replaceDir(c.dir(), func() error {
		if err := c.Install(ctx, server); err != nil {
			return err
		}
		_, err := os.Stat(index)
		return err
	}, func() {}, log)
}

func upgradeWebmailHost(ctx context.Context, dir string, log Log) error {
	w := hostWebmail{deploy: dir, log: log}
	envBefore, err := os.ReadFile(w.envPath())
	if err != nil {
		return err
	}
	return replaceDir(w.dir(), func() error {
		if err := w.Install(ctx); err != nil {
			return err
		}
		// What it reports itself as follows the files.
		env := regexp.MustCompile(`(?m)^IHASMAIL_VERSION=.*$`).ReplaceAllString(string(envBefore), "IHASMAIL_VERSION="+w.Version())
		if err := os.WriteFile(w.envPath(), []byte(env), 0o600); err != nil {
			return err
		}
		return w.Start(ctx)
	}, func() {
		// The old release, with the environment it ran with.
		os.WriteFile(w.envPath(), envBefore, 0o600)
		w.Start(ctx)
	}, log)
}

// answers is whether url answers 200 with want in its body.
func answers(ctx context.Context, url, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
		return fmt.Errorf("%s answered %s without %q", url, resp.Status, want)
	}
	return nil
}
