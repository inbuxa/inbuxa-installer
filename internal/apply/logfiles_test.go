// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Each shape has its own directory, and names the other's as the one a
// moved server's tracer is taken from.
func TestLogDirsFollowTheShape(t *testing.T) {
	if dir, other := logDirs(true); dir != "/var/log/inbuxa" || other != containerLogDir {
		t.Errorf("host: %s, %s", dir, other)
	}
	// On the data volume, which the image's user can write to, and in a
	// directory of its own: x:Log reads every file in it.
	if dir, other := logDirs(false); !strings.HasPrefix(dir, containerDataPath) || dir == strings.TrimSuffix(containerDataPath, "/") || other != hostLogDir {
		t.Errorf("container: %s, %s", dir, other)
	}
	// What a move to the host leaves behind, as tar names it in the volume.
	if got := "./data/" + strings.TrimPrefix(containerLogDir, containerDataPath); got != "./data/logs" {
		t.Errorf("excluded %s", got)
	}
}

// The unit has systemd make the host's directory, under the same name.
func TestUnitMakesTheLogDirectory(t *testing.T) {
	u := hostServer{deploy: "/var/lib/inbuxa"}.unit(true)
	for _, want := range []string{"LogsDirectory=" + filepath.Base(hostLogDir) + "\n", "LogsDirectoryMode=0750\n", "User=" + HostServerUser + "\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

// Only a server installed here, still installed, and without its log files
// is due them.
func TestLogFilesDue(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		st  state.State
		due bool
	}{
		{state.State{Shapes: map[string]string{"server": "container"}}, true},
		{state.State{Shapes: map[string]string{"server": "host", "webmail": "host"}}, true},
		{state.State{Shapes: map[string]string{"server": "host"}, LogFiles: true}, false},
		{state.State{Shapes: map[string]string{"console": "container", "webmail": "container"}, ServerURL: "https://mail.example.org"}, false},
		{state.State{Shapes: map[string]string{"server": "container"}, RemovedAt: &now}, false},
	} {
		if got := LogFilesDue(&c.st); got != c.due {
			t.Errorf("%+v: due = %v", c.st, got)
		}
	}
}
