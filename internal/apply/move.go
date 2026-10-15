// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Moving the mail server between a container and the host, when `install`
// is run again with the other --server.
//
// Its configuration and mail go with it. Only one thing in them names a
// place on disk: config.json, the data store's own description, whose path
// is /var/lib/inbuxa/ in a container and <dir>/server/data/ on the host.
// That is rewritten in the copy. The rest of what differs between the two
// shapes is the server's own settings, which only the server can change, so
// a move needs an API key, as `certs` and `join` do:
//
//   - its listeners: everything on [::] inside a container, plain HTTP on
//     loopback and no HTTPS of its own on the host (jmap.HostListeners);
//   - the addresses exempted from the auto-ban, which are where the front
//     ends' requests arrive from: the project network's in a container,
//     loopback on the host.
//
// Nothing is moved, only copied, and the original is left where it was: the
// volumes after a move to the host, <dir>/server after a move into a
// container. A failure on the way puts the server back as it was, and the
// archive taken first is in <dir>/backups either way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/compose"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

const (
	containerDataPath = "/var/lib/inbuxa/"
	containerUser     = "2000:2000" // the image's inbuxa user
	preMoveDir        = ".pre-move"
)

// ErrMoveNeedsKey is a move asked for without an API key.
var ErrMoveNeedsKey = errors.New("an API key is needed to move the server")

// MovingServer says whether running install with these options moves the
// server between shapes, and from which.
func MovingServer(st *state.State, o plan.Options) (from plan.Shape, moving bool) {
	// Not after an uninstall either: nothing runs to move, and install
	// brings the recorded shape back, or sets up the new one, as it is told.
	if st == nil || !st.Installed() || st.Removed() {
		return "", false
	}
	was, wants := st.Shapes["server"], o.Shape(plan.Server)
	if was == "" || wants == plan.Skip || was == string(wants) {
		return "", false
	}
	return plan.Shape(was), true
}

// MoveNote is what a plan says about a move before anything happens.
func MoveNote(from, to plan.Shape) string {
	return fmt.Sprintf(`Moving the mail server from a %s install to a %s install:
  - everything here stops while it moves, a minute or two
  - its configuration and mail are archived into backups/ first, then copied
  - its listeners and the auto-ban exemptions are changed for the new shape,
    with an API key made in the console (--key - or INBUXA_API_KEY)
  - the %s copy is left where it is; anything failing puts it back as it was
`, from, to, from)
}

// serverMove is a move begun: the old shape stopped and archived, waiting
// for the new shape's files to be written before finish carries it out.
type serverMove struct {
	from, to plan.Shape
	dir      string
	key      string
	local    bool
	cmp      docker.Compose
	log      Log
	archive  string // the archive taken before anything changed
	oldImage string // the server image a container install ran
	done     bool   // finish ran: the move happened, or it put things back itself
}

// beginMove checks the key, stops what runs here and archives the server's
// data. Nothing of the new shape is written yet, so a failure here leaves
// the old one exactly as it was.
func beginMove(ctx context.Context, o plan.Options, from plan.Shape, f host.Facts, log Log) (*serverMove, error) {
	to := o.Shape(plan.Server)
	if o.Key == "" {
		return nil, fmt.Errorf("%w from a %s install to a %s install, as its listeners change with it: "+
			"make one in the console (your account, API keys) and run this again with --key - "+
			"(or INBUXA_API_KEY); it isn't kept", ErrMoveNeedsKey, from, to)
	}
	docker.UseRuntime(f.Runtime)
	m := &serverMove{from: from, to: to, dir: o.Dir, key: o.Key, local: o.Local, log: log,
		cmp: docker.Compose{Dir: o.Dir, Runtime: f.Runtime}}
	srv := &jmap.Client{BaseURL: "http://" + hostFinalBind, Token: o.Key}
	if _, err := srv.Listeners(ctx); err != nil {
		return nil, fmt.Errorf("the server on %s didn't take the API key, so nothing was changed: %w", hostFinalBind, err)
	}

	log.Step("moving the mail server from a %s install to a %s install", from, to)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if err := os.MkdirAll(filepath.Join(m.dir, "backups"), 0o700); err != nil {
		return nil, err
	}
	hasCompose := exists(filepath.Join(m.dir, "compose.yaml"))

	if from == plan.Container {
		b, err := os.ReadFile(filepath.Join(m.dir, "compose.yaml"))
		if err != nil {
			return nil, err
		}
		tag := installedVersion(m.dir, plan.Server, plan.Container, string(b))
		m.oldImage = strings.TrimSuffix(DefaultServerImage, ":"+tagOf(DefaultServerImage)) + ":" + tag
		// The deployment as it is, to put back: the new shape's files are
		// written over these before the move is carried out.
		if err := m.keepDeployment(); err != nil {
			return nil, err
		}
		log.Info("stopping everything in containers here while the server moves")
		if err := m.cmp.Run(ctx, "stop"); err != nil {
			return nil, err
		}
		name, err := archiveVolumes(ctx, m.dir, tag)
		if err != nil {
			m.cmp.Run(ctx, "start")
			return nil, fmt.Errorf("archiving the server's data, so nothing was changed: %w", err)
		}
		m.archive = name
		log.Info("archived its configuration and data to %s", name)
		return m, nil
	}

	// From the host: archived while stopped, then started once more, as it
	// was, to be given the listeners a container needs. They take effect at
	// its next start, which is in the container.
	if hasCompose {
		if err := m.keepDeployment(); err != nil {
			return nil, err
		}
	}
	if err := runQuiet(ctx, "systemctl", "stop", HostServerUnit); err != nil {
		return nil, err
	}
	m.archive = filepath.Join(m.dir, "backups", "server-host-"+stamp+".tar.gz")
	if err := runQuiet(ctx, "tar", "-czf", m.archive, "-C", m.dir, "server"); err != nil {
		runQuiet(ctx, "systemctl", "start", HostServerUnit)
		return nil, fmt.Errorf("archiving the server's data, so nothing was changed: %w", err)
	}
	log.Info("archived its configuration and data to %s", m.archive)
	if err := runQuiet(ctx, "systemctl", "start", HostServerUnit); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, log, "the server to answer again", 120*time.Second, func(ctx context.Context) error {
		return live(ctx, "http://"+hostFinalBind)
	}); err != nil {
		return nil, withLogs(ctx, err, hostServer{deploy: m.dir}, "server")
	}
	if err := srv.ContainerListeners(ctx); err != nil {
		return nil, m.restoreHost(ctx, fmt.Errorf("changing the server's listeners for a container: %w", err))
	}
	log.Info("its listeners set for a container, from its next start")
	log.Info("stopping everything here while the server moves")
	if err := runQuiet(ctx, "systemctl", "disable", "--now", "--quiet", HostServerUnit); err != nil {
		return nil, m.restoreHost(ctx, err)
	}
	if hasCompose {
		m.cmp.Run(ctx, "stop")
	}
	return m, nil
}

// abort puts the old shape back when the install stops between beginMove and
// finish, writing or fetching the new shape's pieces.
func (m *serverMove) abort(ctx context.Context) {
	if m.done {
		return
	}
	m.done = true
	ctx = context.WithoutCancel(ctx)
	if m.from == plan.Container {
		m.log.Info("the move stopped before the server moved: putting it back in its container")
		removeUnit(ctx, HostServerUnit, hostUnitPath)
		m.putDeploymentBack(ctx)
		return
	}
	m.log.Info("the move stopped before the server moved: putting it back on the host")
	m.restoreHost(ctx, errors.New("aborted"))
	m.putDeploymentBack(ctx)
}

// keepDeployment copies the files the new shape is about to be written
// over, so a failed move can put them back.
func (m *serverMove) keepDeployment() error {
	keep := filepath.Join(m.dir, preMoveDir)
	os.RemoveAll(keep)
	if err := os.MkdirAll(keep, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"compose.yaml", "Caddyfile"} {
		b, err := os.ReadFile(filepath.Join(m.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(keep, name), b, 0o640); err != nil {
			return err
		}
	}
	return nil
}

// putDeploymentBack restores what keepDeployment kept, and starts it.
func (m *serverMove) putDeploymentBack(ctx context.Context) {
	keep := filepath.Join(m.dir, preMoveDir)
	for _, name := range []string{"compose.yaml", "Caddyfile"} {
		if b, err := os.ReadFile(filepath.Join(keep, name)); err == nil {
			os.WriteFile(filepath.Join(m.dir, name), b, 0o640)
		} else {
			os.Remove(filepath.Join(m.dir, name))
		}
	}
	if exists(filepath.Join(m.dir, "compose.yaml")) {
		m.cmp.Run(ctx, "up", "-d", "--remove-orphans")
	}
}

// finish carries the move out once the new shape's deployment is written
// and, for the host, its binary, user, directories and environment are in
// place. The server answers in its new shape when it returns nil.
func (m *serverMove) finish(ctx context.Context, stack compose.Stack, snippets bool) error {
	m.done = true
	var err error
	if m.to == plan.Host {
		err = m.toHost(ctx, stack, snippets)
	} else {
		err = m.toContainer(ctx, stack, snippets)
	}
	if err == nil {
		os.RemoveAll(filepath.Join(m.dir, preMoveDir))
	}
	return err
}

func (m *serverMove) toHost(ctx context.Context, stack compose.Stack, snippets bool) error {
	hs := hostServer{deploy: m.dir, log: m.log}
	back := func(why error) error {
		m.log.Info("putting the server back in its container")
		removeUnit(ctx, HostServerUnit, hostUnitPath)
		os.Remove(setupDropIn)
		os.Remove(setupEnvPath)
		m.putDeploymentBack(ctx)
		return fmt.Errorf("%w; the server is back in its container with its data as it was (archived in %s)", why, m.archive)
	}

	m.log.Info("copying its configuration and mail into %s", hs.home())
	if err := m.clearForCopy(ctx, hs); err != nil {
		return back(err)
	}
	stage, err := os.MkdirTemp(m.dir, ".move-")
	if err != nil {
		return back(err)
	}
	defer os.RemoveAll(stage)
	out := exec.CommandContext(ctx, docker.CLI, "run", "--rm", "--user", "0", "--entrypoint", "tar",
		"-v", "inbuxa_inbuxa-etc:/from/etc:ro", "-v", "inbuxa_inbuxa-data:/from/data:ro",
		m.oldImage, "-cf", "-", "-C", "/from", ".")
	in := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", stage)
	if err := pipe(out, in); err != nil {
		return back(fmt.Errorf("copying the server's volumes: %w", err))
	}
	// Renamed into place, not copied: the stage is on the same filesystem,
	// and a large store should not need twice its size free.
	if err := moveContents(filepath.Join(stage, "data"), hs.DataPath()); err != nil {
		return back(err)
	}
	if err := moveContents(filepath.Join(stage, "etc"), hs.home()); err != nil {
		return back(err)
	}
	if err := rewriteDataPath(hs.configPath(), containerDataPath, hs.DataPath()); err != nil {
		return back(err)
	}
	if err := runQuiet(ctx, "chown", "-R", HostServerUser+":"+HostServerUser, hs.home()); err != nil {
		return back(err)
	}

	// The first start is behind the same fence as first boot: its listeners
	// are still a container's, on every interface, until they are fitted.
	m.log.Info("starting it on the host, on loopback only until its listeners are fitted")
	if _, err := hs.WriteUnit(ctx, true); err != nil {
		return back(err)
	}
	if err := hs.Start(ctx, modeFenced, ""); err != nil {
		return back(withLogs(ctx, err, hs, "server"))
	}
	if err := m.cameBack(ctx, hostSetupBind, func() string { return hs.Logs(ctx, "server", 400) }); err != nil {
		return back(withLogs(ctx, err, hs, "server"))
	}
	srv := &jmap.Client{BaseURL: hostSetupBind, Token: m.key}
	if err := srv.HostListeners(ctx, hostFinalBind, m.local); err != nil {
		return back(fmt.Errorf("fitting the server's listeners to the host: %w", err))
	}
	m.log.Info("plain HTTP on %s for the proxy and the webmail; 443 is left to the proxy", hostFinalBind)
	if stack.ServesWebmail() || stack.Proxy || snippets {
		if err := allowOnce(ctx, srv, "127.0.0.1", "the front ends on this machine: their requests arrive from loopback"); err != nil {
			return back(fmt.Errorf("exempting the front ends: %w", err))
		}
	}
	if err := hs.Start(ctx, modeNormal, ""); err != nil {
		return back(withLogs(ctx, err, hs, "server"))
	}
	if err := m.cameBack(ctx, "http://"+hostFinalBind, func() string { return hs.Logs(ctx, "server", 400) }); err != nil {
		return back(withLogs(ctx, err, hs, "server"))
	}
	m.log.Info("the server answers on the host; its volumes are left as they were, for `docker volume rm` once you are sure")
	return nil
}

func (m *serverMove) toContainer(ctx context.Context, stack compose.Stack, snippets bool) error {
	hs := hostServer{deploy: m.dir, log: m.log}
	back := func(why error) error {
		m.log.Info("putting the server back on the host")
		m.cmp.Run(ctx, "rm", "--stop", "--force", "server")
		m.restoreHost(ctx, why)
		m.putDeploymentBack(ctx)
		return fmt.Errorf("%w; the server is back on the host with its data as it was (archived in %s)", why, m.archive)
	}

	// Its volumes, made by compose so they belong to the project. Volumes
	// left from an earlier install in a container hold older data: archived
	// and emptied, never mixed with this.
	if err := m.cmp.Run(ctx, "create", "server"); err != nil {
		return back(err)
	}
	image := DefaultServerImage
	left, _ := docker.Output(ctx, "run", "--rm", "--user", "0", "--entrypoint", "sh",
		"-v", "inbuxa_inbuxa-etc:/to/etc", "-v", "inbuxa_inbuxa-data:/to/data", image,
		"-c", "ls -A /to/etc /to/data | grep -v '^/to\\|^$' | head -1")
	if strings.TrimSpace(left) != "" {
		name, err := archiveVolumes(ctx, m.dir, tagOf(image))
		if err != nil {
			return back(fmt.Errorf("archiving the volumes left from an earlier install: %w", err))
		}
		m.log.Info("the server's volumes held an earlier install's data: archived to %s and emptied", name)
		if _, err := docker.Output(ctx, "run", "--rm", "--user", "0", "--entrypoint", "sh",
			"-v", "inbuxa_inbuxa-etc:/to/etc", "-v", "inbuxa_inbuxa-data:/to/data", image,
			"-c", "find /to/etc /to/data -mindepth 1 -delete"); err != nil {
			return back(err)
		}
	}

	// Only the configuration is staged, to be rewritten; the store goes
	// straight from <dir>/server into its volume.
	m.log.Info("copying its configuration and mail into the volumes")
	stage, err := os.MkdirTemp(m.dir, ".move-")
	if err != nil {
		return back(err)
	}
	defer os.RemoveAll(stage)
	if err := os.MkdirAll(filepath.Join(stage, "etc"), 0o750); err != nil {
		return back(err)
	}
	staged := filepath.Join(stage, "etc", "config.json")
	if err := copyFile(hs.configPath(), staged, 0o640); err != nil {
		return back(err)
	}
	if err := rewriteDataPath(staged, hs.DataPath(), containerDataPath); err != nil {
		return back(err)
	}
	out := exec.CommandContext(ctx, "tar", "-cf", "-", "-C", stage, "etc", "-C", hs.home(), "data")
	in := exec.CommandContext(ctx, docker.CLI, "run", "--rm", "-i", "--user", "0", "--entrypoint", "sh",
		"-v", "inbuxa_inbuxa-etc:/to/etc", "-v", "inbuxa_inbuxa-data:/to/data", image,
		"-c", "tar -xf - -C /to && chown -R "+containerUser+" /to/etc /to/data")
	if err := pipe(out, in); err != nil {
		return back(fmt.Errorf("filling the server's volumes: %w", err))
	}

	m.log.Info("starting it in its container")
	if err := m.cmp.Run(ctx, "up", "-d", "server"); err != nil {
		return back(err)
	}
	url := "http://" + stack.ServerBind
	if err := m.cameBack(ctx, url, func() string { return m.cmp.Logs(ctx, "server", 400) }); err != nil {
		return back(withLogs(ctx, err, m.cmp, "server"))
	}

	// The front ends' requests now arrive from the project network.
	srv := &jmap.Client{BaseURL: url, Token: m.key}
	exempt := map[string]string{}
	if stack.Webmail || stack.WebmailUnit {
		exempt[stack.WebmailIP] = "the webmail: every request arrives from this address"
	}
	if stack.Proxy {
		exempt[stack.CaddyIP] = "the proxy: certificate renewals and autoconfig arrive from this address"
	}
	if snippets {
		exempt[stack.GatewayIP] = "your web server in front of the suite: its requests arrive from this address"
	}
	for addr, why := range exempt {
		if addr == "" {
			continue
		}
		if err := allowOnce(ctx, srv, addr, why); err != nil {
			return back(fmt.Errorf("exempting %s: %w", addr, err))
		}
	}
	if len(exempt) > 0 {
		// They take effect only on a restart.
		if err := m.cmp.Run(ctx, "restart", "server"); err != nil {
			return back(err)
		}
		if err := m.cameBack(ctx, url, func() string { return m.cmp.Logs(ctx, "server", 400) }); err != nil {
			return back(withLogs(ctx, err, m.cmp, "server"))
		}
	}

	// Off the host: the unit and the binaries go, as uninstall takes them.
	removeUnit(ctx, HostServerUnit, hostUnitPath)
	old, _ := filepath.Glob(HostServerBinary + ".*")
	for _, p := range append(old, HostServerBinary) {
		os.Remove(p)
	}
	os.Remove(filepath.Join(m.dir, "server.env"))
	m.log.Info("the server answers in its container; %s is left as it was, to delete once you are sure", hs.home())
	return nil
}

// restoreHost puts the archive taken by beginMove back into <dir>/server and
// starts the server on the host again, as it was before the move began.
func (m *serverMove) restoreHost(ctx context.Context, why error) error {
	home := hostServer{deploy: m.dir}.home()
	os.RemoveAll(home)
	if err := runQuiet(ctx, "tar", "-xzf", m.archive, "-C", m.dir); err != nil {
		return fmt.Errorf("%w; and putting %s back from %s failed too: %v", why, home, m.archive, err)
	}
	runQuiet(ctx, "systemctl", "enable", "--quiet", HostServerUnit)
	runQuiet(ctx, "systemctl", "restart", HostServerUnit)
	return fmt.Errorf("%w; the server was put back on the host as it was", why)
}

// clearForCopy makes sure <dir>/server is empty before the volumes are
// copied into it: what an earlier install on the host left there is
// archived, never mixed with this.
func (m *serverMove) clearForCopy(ctx context.Context, hs hostServer) error {
	entries, _ := os.ReadDir(hs.DataPath())
	if len(entries) == 0 && !exists(hs.configPath()) {
		return nil
	}
	name := filepath.Join(m.dir, "backups", "server-host-earlier-"+time.Now().UTC().Format("20060102T150405Z")+".tar.gz")
	if err := runQuiet(ctx, "tar", "-czf", name, "-C", m.dir, "server"); err != nil {
		return fmt.Errorf("archiving what an earlier install left in %s: %w", hs.home(), err)
	}
	m.log.Info("%s held an earlier install's data: archived to %s and emptied", hs.home(), name)
	os.Remove(hs.configPath())
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(hs.DataPath(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// cameBack waits for the server at url and makes sure it came back with its
// configuration: a server that finds none starts in bootstrap mode and says
// so, which means the copy missed something.
func (m *serverMove) cameBack(ctx context.Context, url string, logs func() string) error {
	if err := waitFor(ctx, m.log, "the server to answer", 120*time.Second, func(ctx context.Context) error {
		return live(ctx, url)
	}); err != nil {
		return err
	}
	if strings.Contains(logs(), "bootstrap mode") {
		return errors.New("the server came back in bootstrap mode, without its configuration")
	}
	return nil
}

// allowOnce exempts an address unless it already is.
func allowOnce(ctx context.Context, srv *jmap.Client, address, reason string) error {
	have, err := srv.AllowedIPs(ctx)
	if err != nil {
		return err
	}
	if have[address] {
		return nil
	}
	return srv.AllowIP(ctx, address, reason)
}

// rewriteDataPath points the data store's description at its new home. Only
// a path under the old home is changed: a store kept elsewhere (PostgreSQL,
// a path the operator chose) is left alone.
func rewriteDataPath(configPath, from, to string) error {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("the server's configuration: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("%s isn't JSON: %w", configPath, err)
	}
	p, ok := cfg["path"].(string)
	if !ok || !strings.HasPrefix(p, strings.TrimSuffix(from, "/")) {
		return nil
	}
	cfg["path"] = strings.TrimSuffix(to, "/") + strings.TrimPrefix(p, strings.TrimSuffix(from, "/"))
	out, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, info.Mode().Perm())
}

// moveContents renames everything in src into dest, which is made if need
// be. Both are on one filesystem.
func moveContents(src, dest string) error {
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pipe runs out | in and reports the first of their failures, with what
// each said on its standard error.
func pipe(out, in *exec.Cmd) error {
	r, w := io.Pipe()
	var outErr, inErr strings.Builder
	out.Stdout, out.Stderr = w, &outErr
	in.Stdin, in.Stderr = r, &inErr
	if err := in.Start(); err != nil {
		return err
	}
	errOut := out.Run()
	w.CloseWithError(errOut)
	errIn := in.Wait()
	switch {
	case errOut != nil:
		return fmt.Errorf("%w: %s", errOut, strings.TrimSpace(outErr.String()))
	case errIn != nil:
		return fmt.Errorf("%w: %s", errIn, strings.TrimSpace(inErr.String()))
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
