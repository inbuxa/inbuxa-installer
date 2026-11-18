// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The server's user can write its home, so it can swap data/ for a symbolic
// link to a directory of root's. Install runs again as root on every
// converge or move, and must not then give that directory to the server's
// user (INSTALLER-1).
func TestServerDirsRefusesALinkedDataDir(t *testing.T) {
	deploy := t.TempDir()
	victim := t.TempDir() // a directory of root's, as far as the installer knows
	h := hostServer{deploy: deploy}
	if err := os.MkdirAll(h.home(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(h.home(), "data")); err != nil {
		t.Fatal(err)
	}

	var chowned []string
	defer func(f func(context.Context, ...string) error) { chownServer = f }(chownServer)
	chownServer = func(_ context.Context, args ...string) error {
		chowned = append(chowned, strings.Join(args, " "))
		return nil
	}

	err := h.serverDirs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a data directory that is a symbolic link should be refused, got %v", err)
	}
	for _, c := range chowned {
		// chown follows a link it is given unless told -h, and always
		// through a trailing slash.
		if !strings.HasPrefix(c, "-h ") || strings.HasSuffix(c, "/") || strings.Contains(c, "/data") {
			t.Errorf("chown %s: would reach the link's target", c)
		}
	}
}

// A first install, and a converge over directories already right, make
// both and give them to the server's user without following anything.
func TestServerDirsMakesBoth(t *testing.T) {
	h := hostServer{deploy: t.TempDir()}
	var chowned []string
	defer func(f func(context.Context, ...string) error) { chownServer = f }(chownServer)
	chownServer = func(_ context.Context, args ...string) error {
		chowned = append(chowned, strings.Join(args, " "))
		return nil
	}
	for range 2 {
		chowned = nil
		if err := h.serverDirs(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"-h inbuxa:inbuxa " + h.home(),
			"-h inbuxa:inbuxa " + filepath.Join(h.home(), "data"),
		}
		if strings.Join(chowned, "|") != strings.Join(want, "|") {
			t.Errorf("chown %v, want %v", chowned, want)
		}
		if fi, err := os.Lstat(filepath.Join(h.home(), "data")); err != nil || !fi.IsDir() {
			t.Errorf("data/ is not a directory: %v", err)
		}
	}
}
