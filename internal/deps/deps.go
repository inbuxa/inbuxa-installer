// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package deps is what the installer does about something the machine needs
// and does not have.
//
// Refusing because Docker is missing is not help; it is a chore handed back
// to the operator, who will then install it in whatever way the first search
// result suggests. The installer knows what it needs, knows this machine, and
// can do it -- so it offers, says exactly what it would run, and does it only
// when told.
//
// What it will install, and from where:
//
//   - the Docker daemon: the distribution's own package, never a third-party
//     apt repository. One package from the archive the machine already
//     trusts beats a new signing key and a new source.
//   - the Compose plugin: Debian's docker.io has no compose v2 at all, so
//     this is the official static binary from Docker's release, pinned by
//     version and verified against a checksum in this source file.
//   - Node: the distribution's, when it is new enough; otherwise the
//     official tarball into /opt, pinned and checksummed the same way. No
//     NodeSource repository either, for the same reason.
//
// Every fetch is verified before anything is put in place. A checksum that
// does not match stops the step; it does not warn and continue.
package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

// Pinned versions. Bumping one means bumping its checksum in the same
// commit: the pair is the point.
const (
	composeVersion = "v5.5.1"
	nodeVersion    = "v22.23.2"
)

var composeSHA = map[string]string{
	"amd64": "db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576",
	"arm64": "732e3a84c1a0f67256ce80bc2598a24546b10ca05f9faa97efceb1171ece2ef7",
}

var nodeSHA = map[string]string{
	"amd64": "d60acfe00a2932254bb0ad20e01b0d74397a0875595de719654b214f4b03f307",
	"arm64": "fff4078c5def658577f92c88db7db3bc0072924bfb93fe52c1e744a54e94abb8",
}

// Need is one missing thing, and what the installer would do about it.
type Need struct {
	Name    string   // "docker", "compose", "node"
	Because string   // the shape that wants it
	Fixable bool     // the installer can do this here
	Why     string   // when it cannot, the reason
	Actions []string // what it would do, in the words the plan shows
	steps   []step   // what it would actually run
}

type step struct {
	title string
	run   func(ctx context.Context, log io.Writer) error
}

// For works out what is missing for a shape, and what could be done about it
// on this machine. An empty result means nothing is in the way.
func For(f host.Facts, wantContainers, wantHostWebmail bool) []Need {
	var needs []Need
	if wantContainers {
		needs = append(needs, runtimeNeeds(f)...)
	}
	if wantHostWebmail && (!f.Node.Present || f.Node.Major < 22) {
		needs = append(needs, nodeNeed(f))
	}
	var out []Need
	for _, n := range needs {
		if n.Name != "" {
			out = append(out, n)
		}
	}
	return out
}

// runtimeNeeds is what this machine is missing before it can run containers.
//
// Which runtime depends on the distribution, and that is the whole point:
// Debian and Arch ship Docker, and the Red Hat family ships podman and no
// Docker at all. Telling a Fedora or Rocky operator to add Docker's own
// repository -- a new source and a new signing key -- to a machine that
// already has a working container runtime would be the wrong trade. Compose
// speaks the Docker API and podman serves it, so one compose file and one
// plugin drive either.
func runtimeNeeds(f host.Facts) []Need {
	if f.Runtime.Usable {
		return nil
	}
	switch {
	// Something is installed and broken in a way that is not ours to fix.
	case f.Runtime.Present && f.Runtime.Compose != "":
		return []Need{{
			Name:    f.Runtime.Kind,
			Because: "a container install",
			Why:     f.Runtime.Why + " -- that is not something this installer should fix for you",
		}}
	case f.Runtime.Kind == "podman":
		// Podman is here; what is missing is its API socket, the compose
		// plugin, or both.
		var needs []Need
		if strings.Contains(f.Runtime.Why, "socket") {
			needs = append(needs, podmanSocketNeed())
		}
		if strings.Contains(f.Runtime.Why, "compose") || len(needs) == 0 {
			needs = append(needs, composeNeed(f))
		}
		return needs
	case f.OS.Family == "rhel":
		// No runtime at all, on a distribution whose own is podman.
		return []Need{podmanNeed(f), podmanSocketNeed(), composeNeed(f)}
	case !f.Runtime.Present:
		return []Need{dockerNeed(f), composeNeed(f)}
	default:
		return []Need{{
			Name:    "docker",
			Because: "a container install",
			Why:     f.Runtime.Why + " -- that is not something this installer should fix for you",
		}}
	}
}

func podmanNeed(f host.Facts) Need {
	n := Need{Name: "podman", Because: "a container install"}
	pkg, install := packageInstall(f.OS.Family, "podman")
	if install == nil {
		n.Why = "this installer does not know how to install podman on " + describeOS(f.OS)
		return n
	}
	n.Fixable = true
	n.Actions = []string{fmt.Sprintf("install %s from the distribution's own archive", pkg)}
	n.steps = []step{{"installing " + pkg, install}}
	return n
}

// podmanSocketNeed turns on the API socket compose talks to. Podman works
// perfectly well without it; compose does not.
func podmanSocketNeed() Need {
	return Need{
		Name:    "podman socket",
		Because: "compose, which speaks the Docker API that this socket serves",
		Fixable: true,
		Actions: []string{"enable and start podman.socket, which serves the API at /run/podman/podman.sock"},
		steps: []step{{"starting podman.socket", func(ctx context.Context, log io.Writer) error {
			return run(ctx, log, "systemctl", "enable", "--now", "podman.socket")
		}}},
	}
}

func dockerNeed(f host.Facts) Need {
	n := Need{Name: "docker", Because: "a container install"}
	pkg, install := packageInstall(f.OS.Family, dockerPackage(f.OS.Family))
	if install == nil {
		n.Why = "this installer does not know how to install a container runtime on " + describeOS(f.OS)
		return n
	}
	n.Fixable = true
	n.Actions = []string{
		fmt.Sprintf("install %s from the distribution's own archive", pkg),
		"enable and start the docker service",
	}
	n.steps = []step{
		{"installing " + pkg, install},
		{"starting docker", func(ctx context.Context, log io.Writer) error {
			return run(ctx, log, "systemctl", "enable", "--now", "docker")
		}},
	}
	return n
}

// composeNeed is the same plugin whichever runtime is underneath: compose
// speaks the Docker API, and podman serves it.
func composeNeed(f host.Facts) Need {
	arch := goarch()
	sum, ok := composeSHA[arch]
	n := Need{Name: "compose", Because: "a container install"}
	if !ok {
		n.Why = "no pinned Compose build for " + arch
		return n
	}
	url := fmt.Sprintf("https://github.com/docker/compose/releases/download/%s/docker-compose-linux-%s",
		composeVersion, archName(arch))
	n.Fixable = true
	n.Actions = []string{
		fmt.Sprintf("fetch the Compose plugin %s (%s) and check it against its pinned checksum", composeVersion, arch),
		"put it at " + host.ComposePluginPath,
	}
	n.steps = []step{
		{"fetching compose " + composeVersion, func(ctx context.Context, log io.Writer) error {
			return fetchVerified(ctx, log, url, sum, host.ComposePluginPath, 0o755)
		}},
	}
	return n
}

func nodeNeed(f host.Facts) Need {
	n := Need{Name: "node", Because: "a host install of the webmail"}
	arch := goarch()
	sum, ok := nodeSHA[arch]
	if !ok {
		n.Why = "no pinned Node build for " + arch
		return n
	}
	url := fmt.Sprintf("https://nodejs.org/dist/%s/node-%s-linux-%s.tar.xz", nodeVersion, nodeVersion, archName2(arch))
	n.Fixable = true
	n.Actions = []string{
		fmt.Sprintf("fetch Node %s (%s) and check it against its pinned checksum", nodeVersion, arch),
		"unpack it into /opt/inbuxa/node, used by the webmail's unit and nothing else",
	}
	n.steps = []step{
		{"fetching node " + nodeVersion, func(ctx context.Context, log io.Writer) error {
			tmp := filepath.Join(os.TempDir(), "inbuxa-node.tar.xz")
			if err := fetchVerified(ctx, log, url, sum, tmp, 0o644); err != nil {
				return err
			}
			if err := os.MkdirAll("/opt/inbuxa/node", 0o755); err != nil {
				return err
			}
			return run(ctx, log, "tar", "-xJf", tmp, "-C", "/opt/inbuxa/node", "--strip-components=1")
		}},
	}
	return n
}

func dockerPackage(family string) string {
	switch family {
	case "debian":
		return "docker.io"
	case "arch":
		return "docker"
	case "suse":
		return "docker"
	}
	// The Red Hat family is deliberately absent: its distributions ship
	// podman, not Docker, and runtimeNeeds sends them there.
	return ""
}

// packageInstall returns the package name and the command that installs it,
// or nil when this installer has nothing to say about the machine's
// packaging.
func packageInstall(family, pkg string) (string, func(context.Context, io.Writer) error) {
	if pkg == "" {
		return "", nil
	}
	switch family {
	case "debian":
		return pkg, func(ctx context.Context, log io.Writer) error {
			if err := run(ctx, log, "apt-get", "update", "-qq"); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, "apt-get", "install", "-y", "-qq", pkg)
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
			return pipe(cmd, log)
		}
	case "arch":
		return pkg, func(ctx context.Context, log io.Writer) error {
			return run(ctx, log, "pacman", "-S", "--noconfirm", "--needed", pkg)
		}
	case "rhel":
		return pkg, func(ctx context.Context, log io.Writer) error {
			return run(ctx, log, "dnf", "install", "-y", pkg)
		}
	case "suse":
		return pkg, func(ctx context.Context, log io.Writer) error {
			return run(ctx, log, "zypper", "--non-interactive", "install", pkg)
		}
	}
	return "", nil
}

// Resolve does what the needs say, in order, writing what it is doing to log.
// It stops at the first failure: a half-resolved machine is worse than one
// that is clearly still missing something.
func Resolve(ctx context.Context, log io.Writer, needs []Need) error {
	for _, n := range needs {
		if !n.Fixable {
			return fmt.Errorf("%s: %s", n.Name, n.Why)
		}
		for _, s := range n.steps {
			fmt.Fprintf(log, "  %s\n", s.title)
			if err := s.run(ctx, log); err != nil {
				return fmt.Errorf("%s: %w", n.Name, err)
			}
		}
	}
	return nil
}

// Describe is the offer, in the words the interface and the plan use.
func Describe(needs []Need) string {
	var b strings.Builder
	for _, n := range needs {
		if n.Fixable {
			fmt.Fprintf(&b, "  %s is missing, for %s. The installer can:\n", n.Name, n.Because)
			for _, a := range n.Actions {
				fmt.Fprintf(&b, "      %s\n", a)
			}
		} else {
			fmt.Fprintf(&b, "  %s is missing, for %s, and cannot be installed here: %s\n", n.Name, n.Because, n.Why)
		}
	}
	return b.String()
}

// Fixable is true when everything in the way can be dealt with.
func Fixable(needs []Need) bool {
	for _, n := range needs {
		if !n.Fixable {
			return false
		}
	}
	return len(needs) > 0
}

func fetchVerified(ctx context.Context, log io.Writer, url, want, dest string, mode os.FileMode) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		os.Remove(tmp)
		return fmt.Errorf("checksum of %s is %s, expected %s -- nothing was installed", url, got, want)
	}
	fmt.Fprintf(log, "    checksum ok (%s…)\n", got[:16])
	return os.Rename(tmp, dest)
}

func run(ctx context.Context, log io.Writer, name string, args ...string) error {
	return pipe(exec.CommandContext(ctx, name, args...), log)
}

func pipe(cmd *exec.Cmd, log io.Writer) error {
	out := &prefixer{w: log}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
	}
	return nil
}

// prefixer indents a command's own output so it is plainly the command
// talking and not the installer.
type prefixer struct {
	w   io.Writer
	buf []byte
}

func (p *prefixer) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := strings.IndexByte(string(p.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(p.buf[:i]), "\r")
		p.buf = p.buf[i+1:]
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(p.w, "    | %s\n", line)
		}
	}
	return len(b), nil
}

func goarch() string { return runtime.GOARCH }

// Docker and Node name the same architectures differently, which is a small
// thing that breaks a download silently if it is guessed.
func archName(a string) string {
	if a == "arm64" {
		return "aarch64"
	}
	return "x86_64"
}

func archName2(a string) string {
	if a == "arm64" {
		return "arm64"
	}
	return "x64"
}

func describeOS(o host.OSInfo) string {
	if o.Pretty != "" {
		return o.Pretty
	}
	if o.ID != "" {
		return o.ID
	}
	return "this distribution"
}
