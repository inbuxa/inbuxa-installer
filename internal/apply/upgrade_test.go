// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

const composeFile = `name: inbuxa

services:
  server:
    image: registry.coffeylabs.org/inbuxa/inbuxa-server:2026.10.12
  console:
    image: registry.coffeylabs.org/inbuxa/inbuxa-admin:2026.10.5
  webmail:
    image: registry.coffeylabs.org/inbuxa/inbuxa-webmail:2026.10.9-g62936ab
  caddy:
    image: docker.io/library/caddy:2-alpine
`

func TestUpgradePlanReadsTheMachine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(composeFile), 0o640)
	st := &state.State{Dir: dir, Shapes: map[string]string{"server": "container", "console": "container", "webmail": "container"}}
	steps := UpgradePlan(st)
	if len(steps) != 3 {
		t.Fatalf("steps = %+v", steps)
	}
	want := map[plan.Component]string{plan.Server: "2026.10.12", plan.Console: "2026.10.5", plan.Webmail: "2026.10.9-g62936ab"}
	for _, s := range steps {
		if s.From != want[s.Component] || s.To != target(s.Component, plan.Container) || !s.Due() {
			t.Errorf("%+v", s)
		}
	}
}

func TestHostVersionsComeFromTheirFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "webmail"), 0o755)
	for file, want := range map[string]string{"27.1-rc.1\n": "27.1-rc.1", "2026.10.12+g6e1a1cb\n": "2026.10.12-g6e1a1cb"} {
		os.WriteFile(filepath.Join(dir, "webmail", "VERSION"), []byte(file), 0o644)
		if got := installedVersion(dir, plan.Webmail, plan.Host, ""); got != want {
			t.Errorf("webmail VERSION %q = %q, want %q", file, got, want)
		}
	}
	os.MkdirAll(filepath.Join(dir, "console"), 0o755)
	os.WriteFile(filepath.Join(dir, "console", releaseMarker), []byte(release.ConsoleSHA256+"\n"), 0o644)
	if got := installedVersion(dir, plan.Console, plan.Host, ""); got != release.ConsoleVersion {
		t.Errorf("console with the pinned checksum = %q", got)
	}
	os.WriteFile(filepath.Join(dir, "console", releaseMarker), []byte("abc 2026.10.5\n"), 0o644)
	if got := installedVersion(dir, plan.Console, plan.Host, ""); got != "2026.10.5" {
		t.Errorf("console from an earlier release = %q", got)
	}
}

func TestSetTagChangesOneServiceOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	os.WriteFile(path, []byte(composeFile), 0o640)
	old, err := setTag(dir, plan.Console, "2026.10.12.2")
	if err != nil || old != "2026.10.5" {
		t.Fatalf("old = %q, %v", old, err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	if !strings.Contains(got, "inbuxa-admin:2026.10.12.2\n") || !strings.Contains(got, "inbuxa-server:2026.10.12\n") ||
		!strings.Contains(got, "caddy:2-alpine") {
		t.Errorf("compose.yaml:\n%s", got)
	}
	if _, err := setTag(dir, plan.Webmail, old); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceDirPutsTheOldOneBack(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "console")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "old"), []byte("1"), 0o644)
	undone := false
	err := replaceDir(dest, func() error {
		os.MkdirAll(dest, 0o755)
		os.WriteFile(filepath.Join(dest, "new"), []byte("2"), 0o644)
		return errors.New("did not answer")
	}, func() { undone = true }, &nopLog{})
	if err == nil || !undone {
		t.Fatalf("err = %v, undone = %v", err, undone)
	}
	if _, err := os.Stat(filepath.Join(dest, "old")); err != nil {
		t.Error("the old release was not put back")
	}
	if _, err := os.Stat(filepath.Join(dest, "new")); err == nil {
		t.Error("the failed release was left in place")
	}
	// ...and a good one replaces it, leaving no .prev
	if err := replaceDir(dest, func() error { return os.MkdirAll(dest, 0o755) }, func() {}, &nopLog{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest + ".prev"); err == nil {
		t.Error("the previous release was kept after a good upgrade")
	}
}

type nopLog struct{}

func (nopLog) Step(string, ...any) {}
func (nopLog) Info(string, ...any) {}
func (nopLog) Out() io.Writer      { return io.Discard }
