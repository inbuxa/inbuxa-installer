// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func serve(t *testing.T, body string) (url, sum string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	h := sha256.Sum256([]byte(body))
	return srv.URL, hex.EncodeToString(h[:])
}

func plantedVictim(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(p, []byte("root's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Node is downloaded and then unpacked as root. At a fixed name in the
// shared temporary directory, a local user could plant the file, or a link,
// before the install, and change what root unpacks after its checksum
// passed (INSTALLER-3). It now goes in a directory of its own, removed after.
func TestNodeTarballIsNotAtAFixedNameInTmp(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("TMPDIR", shared)
	victim := plantedVictim(t)
	for _, name := range []string{"inbuxa-node.tar.xz", "inbuxa-node.tar.xz.part"} {
		if err := os.Symlink(victim, filepath.Join(shared, name)); err != nil {
			t.Fatal(err)
		}
	}
	url, sum := serve(t, "a node tarball")
	path, cleanup, err := fetchToTemp(context.Background(), io.Discard, url, sum, "inbuxa-node.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root's\n" {
		t.Errorf("written through a planted link: %q", b)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("%s should be a plain file: %v %v", path, fi, err)
	}
	dir := filepath.Dir(path)
	if dir == shared {
		t.Errorf("%s is straight in the shared temporary directory", path)
	} else if fi, err := os.Lstat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s should be a directory only its owner can reach: %v %v", dir, fi, err)
	}
	cleanup()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s is left behind", path)
	}
}

// A download is written to a new file, never through whatever is at its
// .part name already.
func TestFetchVerifiedDoesNotWriteThroughAPartLink(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "file")
	victim := plantedVictim(t)
	if err := os.Symlink(victim, dest+".part"); err != nil {
		t.Fatal(err)
	}
	url, sum := serve(t, "the download")
	if err := fetchVerified(context.Background(), io.Discard, url, sum, dest, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root's\n" {
		t.Errorf("written through a planted link: %q", b)
	}
	if fi, err := os.Lstat(dest); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("%s should be a plain file: %v %v", dest, fi, err)
	}
}
