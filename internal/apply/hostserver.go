// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// The mail server on the host: the release binary, a user of its own, a
// directory for its configuration and data, and a systemd unit.
//
// First boot is the same sequence as in a container, and the differences are
// all about what a container would otherwise have contained:
//
//   - Setup runs behind a drop-in that lets the server talk to loopback and
//     nothing else. In a container nothing is published until the compose
//     file says so; on the host the server binds every interface the moment
//     it starts, and during setup it takes the bootstrap credential, then
//     passwords on /jmap. The drop-in lives under /run, so even a setup that
//     stops halfway leaves nothing behind after a reboot.
//   - The credential for each setup step is in a file only root can read,
//     not in the unit, which anyone can.
//   - After setup the listeners are fitted to the host (jmap.HostListeners):
//     plain HTTP on loopback, where the proxy and the webmail reach it, and
//     no HTTPS listener of its own on 443, which is the proxy's.
//   - The finished unit refuses to start without a configuration file, so a
//     server that loses its configuration stays down instead of coming back
//     in bootstrap mode on every interface.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/deps"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/discover"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
)

const (
	// HostServerBinary is where the server's binary goes. Not
	// /usr/local/bin/inbuxa, which is this installer.
	HostServerBinary = "/usr/local/bin/inbuxa-server"
	HostServerUnit   = discover.HostServerUnit
	HostServerUser   = "inbuxa"

	hostUnitPath  = discover.HostServerUnitPath
	setupDropIn   = "/run/systemd/system/" + HostServerUnit + ".d/inbuxa-setup.conf"
	setupEnvPath  = "/run/inbuxa-server-setup.env"
	hostSetupBind = "http://127.0.0.1:8080" // the default listeners, before they are fitted
	hostFinalBind = "127.0.0.1:8081"        // where a container install publishes it, too
)

// setupMode is how the server is started for each step of first boot.
type setupMode int

const (
	modeBootstrap setupMode = iota // the bootstrap credential, loopback only
	modeBasic                      // configured, passwords on /jmap, loopback only
	modeNormal                     // as it will run from now on
)

// hostServer is the server as a host install, rooted in the deployment
// directory: <dir>/server holds config.json and data/, <dir>/server.env what
// the unit reads into the server's environment.
type hostServer struct {
	deploy string
	log    Log
}

func (h hostServer) home() string       { return filepath.Join(h.deploy, "server") }
func (h hostServer) configPath() string { return filepath.Join(h.home(), "config.json") }
func (h hostServer) envPath() string    { return filepath.Join(h.deploy, "server.env") }

// DataPath is the built-in store's directory, given to bootstrap.
func (h hostServer) DataPath() string { return filepath.Join(h.home(), "data") + "/" }

// Install puts the release binary, the user and the directories in place. It
// changes nothing that is already right, so a converge can call it too.
func (h hostServer) Install(ctx context.Context) error {
	if path := os.Getenv("INBUXA_SERVER_BINARY"); path != "" {
		// A server build to test, in place of the release. Said loudly,
		// because nothing vouches for it the way the pinned checksum
		// vouches for a release.
		h.log.Step("installing %s instead of the release, as INBUXA_SERVER_BINARY says (no checksum)", path)
		if out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput(); err != nil {
			return fmt.Errorf("%s does not run here: %s", path, strings.TrimSpace(string(out)))
		}
		if err := runQuiet(ctx, "install", "-m", "0755", path, HostServerBinary); err != nil {
			return err
		}
	} else if v := binaryVersion(ctx, HostServerBinary); strings.Contains(v, release.ServerVersion) {
		h.log.Info("%s is already %s", HostServerBinary, release.ServerVersion)
	} else {
		if err := h.fetchBinary(ctx); err != nil {
			return err
		}
	}

	if err := exec.CommandContext(ctx, "getent", "passwd", HostServerUser).Run(); err != nil {
		if err := runQuiet(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", h.home(), "--shell", nologin(), HostServerUser); err != nil {
			return fmt.Errorf("creating the %s user: %w", HostServerUser, err)
		}
		h.log.Info("user %s (system, no login)", HostServerUser)
	}

	// The deployment directory is root's and holds the credentials, which
	// are 0600 on their own; the server only needs to pass through it to
	// reach its own directory.
	if err := os.MkdirAll(h.deploy, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(h.deploy, 0o711); err != nil {
		return err
	}
	for _, d := range []string{h.home(), h.DataPath()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		if err := runQuiet(ctx, "chown", HostServerUser+":"+HostServerUser, d); err != nil {
			return err
		}
	}
	return nil
}

func (h hostServer) fetchBinary(ctx context.Context) error {
	arch := runtime.GOARCH
	sum, ok := release.ServerSHA256[arch]
	if !ok {
		return fmt.Errorf("inbuxa-server %s has no release binary for %s", release.ServerVersion, arch)
	}
	h.log.Step("fetching inbuxa-server %s for %s", release.ServerVersion, arch)
	work, err := os.MkdirTemp("", "inbuxa-server-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	tarball := filepath.Join(work, "inbuxa.tar.gz")
	if err := deps.FetchVerified(ctx, h.log.Out(), release.ServerTarball(arch), sum, tarball, 0o644); err != nil {
		return err
	}
	if err := runQuiet(ctx, "tar", "-xzf", tarball, "-C", work, "inbuxa"); err != nil {
		return fmt.Errorf("unpacking the release: %w", err)
	}
	// Run before it is put in place: a binary this machine's C library
	// cannot load is caught here, not by a unit that restarts forever.
	got := binaryVersion(ctx, filepath.Join(work, "inbuxa"))
	if !strings.Contains(got, release.ServerVersion) {
		return fmt.Errorf("the release binary does not run here (it said %q); run the server as a container on this machine", got)
	}
	if err := runQuiet(ctx, "install", "-m", "0755", filepath.Join(work, "inbuxa"), HostServerBinary); err != nil {
		return err
	}
	h.log.Info("%s, %s", HostServerBinary, strings.TrimSpace(got))
	return nil
}

// WriteEnv writes what the server reads from its environment, and says
// whether that changed: the server only sees it at a start.
func (h hostServer) WriteEnv(vars map[string]string) (bool, error) {
	keys := make([]string, 0, len(vars))
	for k, v := range vars {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by the inbuxa installer: the mail server's environment.\n")
	b.WriteString("# Read by " + HostServerUnit + " at each start.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, vars[k])
	}
	return writeIfChanged(h.envPath(), b.String(), 0o600)
}

// WriteUnit writes the unit. configured adds the condition that a finished
// install keeps: no configuration file, no start.
func (h hostServer) WriteUnit(ctx context.Context, configured bool) error {
	cond := ""
	if configured {
		cond = "# A server that has lost its configuration stays down rather than coming\n" +
			"# back in bootstrap mode.\n" +
			"ConditionPathExists=" + h.configPath() + "\n"
	}
	unit := fmt.Sprintf(`# Written by the inbuxa installer. Changes here are replaced on its next run.
[Unit]
Description=inbuxa mail server
Documentation=https://docs.inbuxa.org/
After=network-online.target
Wants=network-online.target
%s
[Service]
Type=simple
User=%s
Group=%s
EnvironmentFile=%s
ExecStart=%s --config %s
WorkingDirectory=%s
# The mail ports and 443 are below 1024; this lets an unprivileged user
# bind them, and survives the binary being replaced, which setcap does not.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true
LimitNOFILE=65536
KillSignal=SIGINT
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, cond, HostServerUser, HostServerUser, h.envPath(), HostServerBinary, h.configPath(), h.home())
	changed, err := writeIfChanged(hostUnitPath, unit, 0o644)
	if err != nil {
		return err
	}
	if changed {
		if err := runQuiet(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	return runQuiet(ctx, "systemctl", "enable", "--quiet", HostServerUnit)
}

// Start (re)starts the server for a step of first boot, or as it runs from
// then on.
func (h hostServer) Start(ctx context.Context, mode setupMode, bootPass string) error {
	switch mode {
	case modeNormal:
		os.Remove(setupDropIn)
		os.Remove(setupEnvPath)
	default:
		env := "INBUXA_HTTP_BASIC_AUTH=all\n"
		if mode == modeBootstrap {
			env = "INBUXA_RECOVERY_ADMIN=admin:" + bootPass + "\n"
		}
		if err := os.WriteFile(setupEnvPath, []byte(env), 0o600); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(setupDropIn), 0o755); err != nil {
			return err
		}
		dropIn := "# The inbuxa installer, during first boot only. Gone at the next reboot.\n" +
			"[Service]\n" +
			"EnvironmentFile=" + setupEnvPath + "\n" +
			"IPAddressAllow=localhost\n" +
			"IPAddressDeny=any\n"
		if err := os.WriteFile(setupDropIn, []byte(dropIn), 0o644); err != nil {
			return err
		}
	}
	if err := runQuiet(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return runQuiet(ctx, "systemctl", "restart", HostServerUnit)
}

// Restart restarts the server as it is.
func (h hostServer) Restart(ctx context.Context) error {
	return runQuiet(ctx, "systemctl", "restart", HostServerUnit)
}

// Logs is the server's last lines from the journal, for an error message.
// The service argument is there so it reads like a compose project's.
func (h hostServer) Logs(ctx context.Context, _ string, lines int) string {
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", HostServerUnit,
		"-n", fmt.Sprint(lines), "--no-pager", "-o", "cat").CombinedOutput()
	return string(out)
}

// systemCABundle is this machine's trust store, which a private ACME CA's
// root is added to for a server on the host, as it is to the image's for a
// server in a container.
func systemCABundle() ([]byte, error) {
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt", // Debian, Ubuntu, Arch
		"/etc/pki/tls/certs/ca-bundle.crt",   // Fedora, Rocky
		"/etc/ssl/ca-bundle.pem",             // openSUSE
	} {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("no system CA bundle found")
}

// binaryVersion is what a binary says --version is, or what it said instead
// when it could not run: a missing library is reported in that output.
func binaryVersion(ctx context.Context, path string) string {
	out, _ := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	return strings.TrimSpace(string(out))
}

func nologin() string {
	for _, p := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/usr/bin/nologin"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/bin/false"
}

func writeIfChanged(path, content string, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		return false, os.Chmod(path, mode)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return false, err
	}
	return true, os.Chmod(path, mode)
}

func runQuiet(ctx context.Context, name string, args ...string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}
