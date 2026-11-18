// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package relay

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The installed binary is the relay user's to replace (an upgrade from the
// console does), so whatever is there may be that user's own program. relay
// status and relay install, which run as root, ask it its version, and must
// not run it as root to do so (INSTALLER-2). Here the stand-in records that
// it ran: run as the relay user through runuser, it never does, since this
// test is not root or the relay user cannot reach a file in its temp dir.
func TestInstalledVersionDoesNotRunTheBinaryItself(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "inbuxa-relay")
	script := "#!/bin/sh\ntouch " + ran + "\necho inbuxa-relay 27.1.0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	installedVersion(context.Background(), bin)
	if _, err := os.Stat(ran); err == nil {
		t.Error("installedVersion ran the relay user's binary as the installer's own user")
	}
}

// Everything the installer runs of the installed binary goes through
// runuser as the relay user, in a session of its own.
func TestTheInstalledBinaryRunsAsTheRelayUser(t *testing.T) {
	for _, cmd := range []*exec.Cmd{
		asRelay(context.Background(), "status", "--state", StateDir),
		asRelayRun(context.Background(), Binary, "--version"),
	} {
		if len(cmd.Args) < 5 || filepath.Base(cmd.Path) != "runuser" ||
			strings.Join(cmd.Args[:5], " ") != "runuser -u "+User+" -- "+Binary {
			t.Errorf("runs %v, not the binary as %s", cmd.Args, User)
		}
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
			t.Errorf("%v keeps the installer's controlling terminal", cmd.Args)
		}
	}
}
