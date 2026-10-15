// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

func TestAMoveIsAnotherServerShapeOnAnInstalledMachine(t *testing.T) {
	opts := func(s plan.Shape) plan.Options {
		return plan.Options{Choices: []plan.Choice{{Component: plan.Server, Shape: s}}}
	}
	installed := &state.State{Shapes: map[string]string{"server": "container", "webmail": "container"}}
	if from, ok := MovingServer(installed, opts(plan.Host)); !ok || from != plan.Container {
		t.Errorf("container to host: from=%q ok=%v", from, ok)
	}
	if _, ok := MovingServer(installed, opts(plan.Container)); ok {
		t.Error("the same shape is a converge, not a move")
	}
	if _, ok := MovingServer(installed, opts(plan.Skip)); ok {
		t.Error("skipping the server is not a move")
	}
	if _, ok := MovingServer(&state.State{}, opts(plan.Host)); ok {
		t.Error("nothing installed: a first install, not a move")
	}
	removed := time.Now()
	gone := &state.State{Shapes: map[string]string{"server": "container"}, RemovedAt: &removed}
	if _, ok := MovingServer(gone, opts(plan.Host)); ok {
		t.Error("after an uninstall nothing runs to move")
	}
	frontEnds := &state.State{Shapes: map[string]string{"webmail": "container"}}
	if _, ok := MovingServer(frontEnds, opts(plan.Host)); ok {
		t.Error("a machine with only front ends has no server to move")
	}
}

func TestOnlyTheStoresPathUnderTheOldHomeIsRewritten(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	write := func(v map[string]any) {
		b, _ := json.Marshal(v)
		os.WriteFile(cfg, b, 0o640)
	}
	read := func() map[string]any {
		b, _ := os.ReadFile(cfg)
		var v map[string]any
		json.Unmarshal(b, &v)
		return v
	}

	write(map[string]any{"@type": "RocksDb", "path": "/var/lib/inbuxa/", "poolWorkers": 4.0})
	if err := rewriteDataPath(cfg, containerDataPath, "/var/lib/inbuxa/server/data/"); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got["path"] != "/var/lib/inbuxa/server/data/" || got["poolWorkers"] != 4.0 || got["@type"] != "RocksDb" {
		t.Errorf("into the host: %v", got)
	}
	if err := rewriteDataPath(cfg, "/var/lib/inbuxa/server/data/", containerDataPath); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["path"] != "/var/lib/inbuxa/" {
		t.Errorf("back into a container: %v", got)
	}
	if info, _ := os.Stat(cfg); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the file's own", info.Mode().Perm())
	}

	// A store somewhere else is the operator's, and stays where it is.
	write(map[string]any{"@type": "RocksDb", "path": "/srv/mail/store/"})
	rewriteDataPath(cfg, containerDataPath, "/var/lib/inbuxa/server/data/")
	if got := read(); got["path"] != "/srv/mail/store/" {
		t.Errorf("a store elsewhere moved: %v", got)
	}
	write(map[string]any{"@type": "PostgreSql", "host": "db"})
	rewriteDataPath(cfg, containerDataPath, "/var/lib/inbuxa/server/data/")
	if got := read(); got["path"] != nil || got["host"] != "db" {
		t.Errorf("a database store changed: %v", got)
	}
}

func TestMovedContentsArriveAndLeaveTheStageEmpty(t *testing.T) {
	src, dest := t.TempDir(), filepath.Join(t.TempDir(), "made")
	os.MkdirAll(filepath.Join(src, "rocks"), 0o750)
	os.WriteFile(filepath.Join(src, "rocks", "CURRENT"), []byte("MANIFEST-1\n"), 0o640)
	os.WriteFile(filepath.Join(src, "top"), []byte("x"), 0o600)
	if err := moveContents(src, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "rocks", "CURRENT")); err != nil || string(b) != "MANIFEST-1\n" {
		t.Errorf("CURRENT: %q %v", b, err)
	}
	if info, err := os.Stat(filepath.Join(dest, "top")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("top: %v %v", info, err)
	}
	if left, _ := os.ReadDir(src); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if err := moveContents(filepath.Join(src, "absent"), dest); err != nil {
		t.Errorf("nothing to move is not an error: %v", err)
	}
}
