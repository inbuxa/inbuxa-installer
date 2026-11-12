// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

// Joining a node to a cluster is, on the node, a small and mechanical thing:
// point the server at the shared store instead of its built-in one, put the
// secrets every node reads from disk in place, say which role it takes and
// what it must start after, and restart. This file is that, and nothing more.
//
// It is deliberately the node's half only. The shared registry -- which blob
// store, which coordinator, which roles exist -- is set up once for the whole
// cluster, not by every node that joins; a node that joins an existing cluster
// reads all of that from the shared store the moment its config points there,
// needing only the secret files the registry's File references name. So this
// places those files and writes the store config, and leaves the registry to
// the step that creates the cluster.
//
// Everything it would do is worked out first as a Plan, which --dry-run prints
// and Apply carries out: what is shown is what runs. Nothing here reaches
// another machine; the file is copied across and run on each node by whoever
// owns it, exactly as the rest of the installer works.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/topology"
)

// Config is what a join needs to know about this machine: the cluster block
// from the topology, and where this installation keeps the server's files.
type Config struct {
	Node    topology.ClusterNode                                         // this node's cluster block
	Machine string                                                       // the machine's name, used as the hostname when the block gives none
	EtcDir  string                                                       // where config.json and the secret files live (the server's home)
	Unit    string                                                       // the server's systemd unit, e.g. "inbuxa-server.service"
	UnitDir string                                                       // where units live, so drop-ins go in <UnitDir>/<Unit>.d
	User    string                                                       // the server's user, for ownership of what is written
	DryRun  bool                                                         //
	Run     func(ctx context.Context, name string, args ...string) error // exec; nil uses the real one
	Log     func(format string, a ...any)                                // progress; nil is silent
}

// write is one file the join would put in place, rendered up front so a dry
// run shows its exact contents.
type write struct {
	Path    string
	Mode    os.FileMode
	Desc    string
	Content string // for a plain write
	From    string // for an install from a source file; Content is then empty
	Secret  bool   // a secret file: a dry run names it but does not show it
}

// Plan is everything a join would do on this node, in the order it would do
// it, without doing any of it.
type Plan struct {
	Config  Config
	Writes  []write
	Backup  string // the existing config.json copied aside first, for rollback; "" if none
	Restart bool   // the server is restarted at the end
	Role    string // the role the node takes, "" for the node that carries everything
}

// defaultRun runs a command and returns its combined output on failure.
func defaultRun(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (c Config) run() func(ctx context.Context, name string, args ...string) error {
	if c.Run != nil {
		return c.Run
	}
	return defaultRun
}

func (c Config) log(format string, a ...any) {
	if c.Log != nil {
		c.Log(format, a...)
	}
}

func (c Config) configPath() string { return filepath.Join(c.EtcDir, "config.json") }
func (c Config) dropInDir() string  { return filepath.Join(c.UnitDir, c.Unit+".d") }
func (c Config) secretPath(f string) string {
	// A secret_file may be a bare name (kept beside config.json) or an
	// absolute path the operator chose; honour both.
	if filepath.IsAbs(f) {
		return f
	}
	return filepath.Join(c.EtcDir, f)
}

// hostname is what the node calls itself: the block's, or the machine name.
func (c Config) hostname() string {
	if c.Node.Hostname != "" {
		return c.Node.Hostname
	}
	return c.Machine
}

// BuildPlan works out the join without touching the machine. It fails only on
// what it can tell is wrong from here -- a secret with no source that is not
// already in place -- leaving the addresses themselves for the server to judge
// when it starts.
func BuildPlan(c Config) (*Plan, error) {
	if c.EtcDir == "" || c.Unit == "" || c.UnitDir == "" {
		return nil, fmt.Errorf("a join needs the server's directory, unit and unit directory")
	}
	if _, err := os.Stat(c.EtcDir); err != nil {
		return nil, fmt.Errorf("the server's directory %s is not there; a node joins a cluster after the server is installed on it", c.EtcDir)
	}
	p := &Plan{Config: c, Role: c.Node.Role, Restart: true}

	// The secrets every node reads from disk. The store's, then the
	// coordinator's and the blob store's when the block names them. A secret
	// with a source is installed from it; one without must already be there.
	type sec struct{ file, from, what string }
	secs := []sec{{c.Node.Store.SecretFile, c.Node.Store.SecretFrom, "the store password"}}
	if cs := c.Node.Coordinator; cs != nil {
		secs = append(secs, sec{cs.SecretFile, cs.SecretFrom, "the coordinator secret"})
	}
	if b := c.Node.BlobStore; b != nil {
		secs = append(secs, sec{b.SecretFile, b.SecretFrom, "the blob store secret"})
	}
	for _, s := range secs {
		dst := c.secretPath(s.file)
		if s.from == "" {
			if _, err := os.Stat(dst); err != nil {
				return nil, fmt.Errorf("%s is not at %s and no secret_from says where to install it from", s.what, dst)
			}
			continue // already in place, left as it is
		}
		if _, err := os.Stat(s.from); err != nil {
			return nil, fmt.Errorf("%s: the source %s is not there", s.what, s.from)
		}
		p.Writes = append(p.Writes, write{Path: dst, Mode: 0o400, Desc: s.what, From: s.from, Secret: true})
	}

	// The existing config.json, kept aside once so a node can be put back on
	// its old store. Only when there is one and no copy has been made before.
	if _, err := os.Stat(c.configPath()); err == nil {
		backup := c.configPath() + ".prev"
		if _, err := os.Stat(backup); err != nil {
			p.Backup = backup
		}
	}

	cfg, err := storeConfig(c.Node.Store, c.secretPath(c.Node.Store.SecretFile))
	if err != nil {
		return nil, err
	}
	p.Writes = append(p.Writes, write{Path: c.configPath(), Mode: 0o600, Desc: "the shared store this node reads and writes", Content: cfg})

	p.Writes = append(p.Writes, write{
		Path: filepath.Join(c.dropInDir(), "cluster.conf"), Mode: 0o644,
		Desc: "the node's hostname" + roleWord(c.Node.Role), Content: clusterDropIn(c.hostname(), c.Node.Role),
	})
	if len(c.Node.After) > 0 {
		p.Writes = append(p.Writes, write{
			Path: filepath.Join(c.dropInDir(), "cluster-order.conf"), Mode: 0o644,
			Desc: "start this node after " + strings.Join(c.Node.After, ", "), Content: orderDropIn(c.Node.After),
		})
	}
	return p, nil
}

// storeConfig is the server's config.json for a shared SQL store: the one
// store kind more than one node can share. The field names are the server's
// SQL-store config; the type it is given is the server's own store @type.
func storeConfig(s topology.ClusterStore, secretPath string) (string, error) {
	pool := s.PoolMax
	if pool == 0 {
		pool = 40
	}
	cfg := map[string]any{
		"@type":              s.Type,
		"host":               s.Host,
		"port":               s.Port,
		"database":           s.Database,
		"authUsername":       s.User,
		"authSecret":         map[string]any{"@type": "File", "filePath": secretPath},
		"useTls":             s.TLS,
		"poolMaxConnections": pool,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func roleWord(role string) string {
	if role == "" {
		return ""
	}
	return " and role " + role
}

// clusterDropIn sets the node's hostname, and its role unless it is the node
// that carries every task (no role). A drop-in, so the server's own
// environment file is left as it is.
func clusterDropIn(hostname, role string) string {
	var b strings.Builder
	b.WriteString("# Written by the inbuxa installer: this node's place in the cluster.\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "Environment=INBUXA_HOSTNAME=%s\n", hostname)
	if role != "" {
		fmt.Fprintf(&b, "Environment=INBUXA_ROLE=%s\n", role)
	}
	return b.String()
}

// orderDropIn makes the node start after the units that bring up the path to
// its store, so it does not come up before the data layer it depends on.
func orderDropIn(after []string) string {
	units := strings.Join(after, " ")
	var b strings.Builder
	b.WriteString("# Written by the inbuxa installer: reach the data layer before starting.\n")
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "After=%s\n", units)
	fmt.Fprintf(&b, "Wants=%s\n", units)
	return b.String()
}

// Apply carries out the plan: the secrets, then the store config, then the
// drop-ins, then a reload and restart. The order matters -- the config and its
// secret are in place before the server is told to read them.
func Apply(ctx context.Context, p *Plan) error {
	c := p.Config
	run := c.run()

	if p.Backup != "" {
		c.log("keeping the old store config at %s", p.Backup)
		if !c.DryRun {
			if err := copyFile(c.configPath(), p.Backup, 0o600); err != nil {
				return fmt.Errorf("saving the old config aside: %w", err)
			}
			if c.User != "" {
				chown(ctx, run, c.User, p.Backup)
			}
		}
	}

	// Drop-in directory first, so the files below have somewhere to land.
	if !c.DryRun {
		if err := os.MkdirAll(c.dropInDir(), 0o755); err != nil {
			return err
		}
	}

	for _, w := range p.Writes {
		what := w.Path
		if w.Secret {
			what += " (secret, mode " + w.Mode.String() + ")"
		}
		c.log("writing %s -- %s", what, w.Desc)
		if c.DryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(w.Path), 0o750); err != nil {
			return err
		}
		if w.From != "" {
			if err := copyFile(w.From, w.Path, w.Mode); err != nil {
				return fmt.Errorf("installing %s from %s: %w", w.Path, w.From, err)
			}
		} else if err := os.WriteFile(w.Path, []byte(w.Content), w.Mode); err != nil {
			return err
		}
		// Secrets and the store config are the server's to read; the drop-ins
		// are root's, as systemd wants them.
		if c.User != "" && withinEtc(c, w.Path) {
			chown(ctx, run, c.User, w.Path)
		}
	}

	if c.DryRun {
		c.log("dry run: nothing was written, and the server was not restarted")
		return nil
	}

	c.log("reloading systemd and restarting %s", c.Unit)
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if p.Restart {
		if err := run(ctx, "systemctl", "restart", c.Unit); err != nil {
			return err
		}
	}
	return nil
}

// withinEtc is whether a path is one of the server's files (the store config
// or a secret beside it), as opposed to a systemd drop-in.
func withinEtc(c Config, path string) bool {
	rel, err := filepath.Rel(c.EtcDir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func chown(ctx context.Context, run func(context.Context, string, ...string) error, user, path string) {
	// Best effort: ownership needs root, and a dry run or a test without it
	// should not fail over it. The mode is what keeps a secret unreadable.
	_ = run(ctx, "chown", user+":"+user, path)
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// String renders the plan the way --dry-run shows it, in the installer's
// house style.
func (p *Plan) String() string {
	var b strings.Builder
	c := p.Config
	fmt.Fprintf(&b, "Join %s to the cluster as %s\n\n", c.hostname(), roleOrEverything(p.Role))
	n := 0
	step := func(title string, detail ...string) {
		n++
		fmt.Fprintf(&b, "%2d. %s\n", n, title)
		for _, d := range detail {
			fmt.Fprintf(&b, "      %s\n", d)
		}
	}
	if p.Backup != "" {
		step("Keep the current store config for rollback", p.Backup)
	}
	var secrets, files, dropins []write
	for _, w := range p.Writes {
		switch {
		case w.Secret:
			secrets = append(secrets, w)
		case strings.HasSuffix(w.Path, ".conf"):
			dropins = append(dropins, w)
		default:
			files = append(files, w)
		}
	}
	if len(secrets) > 0 {
		detail := make([]string, len(secrets))
		for i, w := range secrets {
			detail[i] = fmt.Sprintf("%s, %s, from %s", w.Path, w.Mode, w.From)
		}
		step("Install the secrets this node reads from disk", detail...)
	}
	for _, w := range files {
		step("Point the server at the shared store", append([]string{w.Path + ":"}, indent(w.Content)...)...)
	}
	for _, w := range dropins {
		step("Configure the node's unit ("+filepath.Base(w.Path)+")", indent(w.Content)...)
	}
	if p.Restart {
		step("Reload systemd and restart the server", c.Unit)
	}
	return b.String()
}

func roleOrEverything(role string) string {
	if role == "" {
		return "the node that carries every task (no role)"
	}
	return "role " + role
}

func indent(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, line)
	}
	return out
}
