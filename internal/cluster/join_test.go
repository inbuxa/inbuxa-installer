// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/topology"
)

// recorder is a Run stub that remembers the commands Apply ran, so a test can
// check that it reloaded and restarted without touching the real systemd.
type recorder struct{ cmds []string }

func (r *recorder) run(_ context.Context, name string, args ...string) error {
	r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
	return nil
}

// fixture lays out a server directory with a password source and an existing
// config.json, and returns a Config rooted entirely in a temp tree.
func fixture(t *testing.T, node topology.ClusterNode) (Config, *recorder) {
	t.Helper()
	root := t.TempDir()
	etc := filepath.Join(root, "server")
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	// An existing single-node config, so a backup is made.
	if err := os.WriteFile(filepath.Join(etc, "config.json"), []byte(`{"@type":"RocksDb"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	return Config{
		Node:    node,
		Machine: "mx2",
		EtcDir:  etc,
		Unit:    "inbuxa-server.service",
		UnitDir: filepath.Join(root, "systemd"),
		User:    "", // no chown in tests: ownership needs root
		Run:     rec.run,
	}, rec
}

// clusterNode is a full cluster block with the password sources written to
// disk, for a happy-path test.
func clusterNode(t *testing.T, dir string) topology.ClusterNode {
	t.Helper()
	for name, body := range map[string]string{"pg.src": "pgpw", "nats.src": "natspw", "s3.src": "s3key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return topology.ClusterNode{
		Store: topology.ClusterStore{
			Type: "PostgreSql", Host: "10.0.0.2", Port: 5000, Database: "inbuxa", User: "inbuxa",
			SecretFile: "pg.pw", SecretFrom: filepath.Join(dir, "pg.src"),
		},
		Coordinator: &topology.ClusterService{
			Addr: "10.0.0.2:4223", User: "inbuxa",
			SecretFile: "nats.pw", SecretFrom: filepath.Join(dir, "nats.src"),
		},
		BlobStore: &topology.ClusterBlob{
			Endpoint: "http://10.0.0.2:3910", Bucket: "blobs",
			SecretFile: "s3.secret", SecretFrom: filepath.Join(dir, "s3.src"),
		},
		Role:  "peer",
		After: []string{"haproxy.service", "wg.service"},
	}
}

func TestBuildPlanAndApply(t *testing.T) {
	src := t.TempDir()
	node := clusterNode(t, src)
	cfg, rec := fixture(t, node)

	p, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Backup == "" {
		t.Error("an existing config.json should be kept aside for rollback")
	}
	if err := Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	// The store config points at the shared store, with the server's own field
	// names and a File secret at the installed path.
	b, err := os.ReadFile(cfg.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("config.json is not JSON: %v", err)
	}
	if got["@type"] != "PostgreSql" || got["host"] != "10.0.0.2" || got["poolMaxConnections"].(float64) != 40 {
		t.Errorf("store config wrong: %v", got)
	}
	as, _ := got["authSecret"].(map[string]any)
	if as["@type"] != "File" || as["filePath"] != filepath.Join(cfg.EtcDir, "pg.pw") {
		t.Errorf("authSecret should be a File at the installed path, got %v", as)
	}

	// The three secrets are installed, each 0400.
	for _, f := range []string{"pg.pw", "nats.pw", "s3.secret"} {
		fi, err := os.Stat(filepath.Join(cfg.EtcDir, f))
		if err != nil {
			t.Fatalf("secret %s not installed: %v", f, err)
		}
		if fi.Mode().Perm() != 0o400 {
			t.Errorf("%s mode is %v, want 0400", f, fi.Mode().Perm())
		}
	}

	// The rollback copy holds the old config.
	if b, _ := os.ReadFile(p.Backup); !strings.Contains(string(b), "RocksDb") {
		t.Error("the backup should hold the previous config")
	}

	// The drop-ins carry the hostname, the role and the boot order.
	dropIn, _ := os.ReadFile(filepath.Join(cfg.dropInDir(), "cluster.conf"))
	if !strings.Contains(string(dropIn), "INBUXA_HOSTNAME=mx2") || !strings.Contains(string(dropIn), "INBUXA_ROLE=peer") {
		t.Errorf("cluster.conf wrong:\n%s", dropIn)
	}
	order, _ := os.ReadFile(filepath.Join(cfg.dropInDir(), "cluster-order.conf"))
	if !strings.Contains(string(order), "After=haproxy.service wg.service") {
		t.Errorf("cluster-order.conf wrong:\n%s", order)
	}

	// It reloaded systemd and restarted the server, and nothing else.
	want := []string{"systemctl daemon-reload", "systemctl restart inbuxa-server.service"}
	if strings.Join(rec.cmds, "|") != strings.Join(want, "|") {
		t.Errorf("commands run: %v, want %v", rec.cmds, want)
	}
}

func TestNoRoleOmitsRoleLine(t *testing.T) {
	src := t.TempDir()
	node := clusterNode(t, src)
	node.Role = "" // the node that carries everything
	cfg, _ := fixture(t, node)
	p, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	dropIn, _ := os.ReadFile(filepath.Join(cfg.dropInDir(), "cluster.conf"))
	if strings.Contains(string(dropIn), "INBUXA_ROLE") {
		t.Errorf("a node with no role should get no INBUXA_ROLE line:\n%s", dropIn)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	src := t.TempDir()
	cfg, rec := fixture(t, clusterNode(t, src))
	cfg.DryRun = true
	p, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// config.json is untouched (still the RocksDb one), no secret was written,
	// no drop-in directory, and no command ran.
	if b, _ := os.ReadFile(cfg.configPath()); !strings.Contains(string(b), "RocksDb") {
		t.Error("a dry run must not rewrite config.json")
	}
	if _, err := os.Stat(filepath.Join(cfg.EtcDir, "pg.pw")); err == nil {
		t.Error("a dry run must not install secrets")
	}
	if _, err := os.Stat(cfg.dropInDir()); err == nil {
		t.Error("a dry run must not create the drop-in directory")
	}
	if len(rec.cmds) != 0 {
		t.Errorf("a dry run must run no commands, ran %v", rec.cmds)
	}
}

func TestMissingSecretSource(t *testing.T) {
	cfg, _ := fixture(t, topology.ClusterNode{
		Store: topology.ClusterStore{
			Type: "PostgreSql", Host: "h", Port: 5000, Database: "inbuxa", User: "inbuxa",
			SecretFile: "pg.pw", // no SecretFrom and not on disk
		},
	})
	if _, err := BuildPlan(cfg); err == nil || !strings.Contains(err.Error(), "secret_from") {
		t.Errorf("a secret with no source and no file should fail, got %v", err)
	}
}

func TestSecretAlreadyInPlace(t *testing.T) {
	cfg, _ := fixture(t, topology.ClusterNode{
		Store: topology.ClusterStore{
			Type: "PostgreSql", Host: "h", Port: 5000, Database: "inbuxa", User: "inbuxa",
			SecretFile: "pg.pw", // already there, no source needed
		},
	})
	if err := os.WriteFile(filepath.Join(cfg.EtcDir, "pg.pw"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(cfg)
	if err != nil {
		t.Fatalf("a secret already in place should be fine: %v", err)
	}
	for _, w := range p.Writes {
		if w.Secret {
			t.Error("a secret already in place should not be re-installed")
		}
	}
}

func TestNoServerDir(t *testing.T) {
	cfg := Config{Node: topology.ClusterNode{}, EtcDir: filepath.Join(t.TempDir(), "absent"), Unit: "u", UnitDir: "d"}
	if _, err := BuildPlan(cfg); err == nil {
		t.Error("a join before the server is installed should fail")
	}
}
