// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"os"
	"path/filepath"
	"testing"
)

// The setup's compose overrides are given to docker compose as root. Put at a
// fixed name in the shared temporary directory, they could be planted
// beforehand by any local user: as a link, to have root write over another
// file, or as their own file, to rewrite it after root wrote it
// (INSTALLER-3). Each now goes in a directory of its own that only root can
// reach, and is gone after.
func TestSetupOverrideIsNotAtAFixedNameInTmp(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("TMPDIR", shared)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("root's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inbuxa-bootstrap.yaml", "inbuxa-basic-auth.yaml"} {
		if err := os.Symlink(victim, filepath.Join(shared, name)); err != nil {
			t.Fatal(err)
		}
	}

	path, cleanup, err := setupOverride("inbuxa-bootstrap.yaml", "services: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root's\n" {
		t.Errorf("written through a planted link: %q", b)
	}
	if b, _ := os.ReadFile(path); string(b) != "services: {}\n" {
		t.Errorf("the override holds %q", b)
	}
	dir := filepath.Dir(path)
	if dir == shared {
		t.Errorf("%s is straight in the shared temporary directory", path)
	} else if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s should be a directory only its owner can reach: %v %v", dir, fi, err)
	}
	cleanup()
	if _, err := os.Lstat(dir); err == nil && dir != shared {
		t.Errorf("%s is left behind", dir)
	}
}
