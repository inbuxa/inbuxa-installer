// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Log files, which the console's Logs page reads (#70).
//
// Bootstrap gives the server one tracer, stdout, and the server adds its own
// file tracer only when it has none, so an installed server used to write no
// files and the Logs page was always empty. A Log tracer is added beside the
// stdout one, with the server's own defaults: inbuxa.log, rotated daily, and
// deleted after inbuxa:LogSettings.keepForDays (30 on a new install).
//
// Where the files go depends on the shape:
//
//   - on the host, /var/log/inbuxa, the server's own default. The unit's
//     LogsDirectory= makes it at each start, owned by the server's user; it
//     is made here too, so a server that is not restarted can write there
//     at once.
//   - in a container, /var/lib/inbuxa/logs on the data volume. The image
//     has no /var/log/inbuxa and runs as an unprivileged user, so a volume
//     mounted there would be root's and unwritable. The data volume is the
//     server's own, and the server keeps its snapshots/ beside its store in
//     the same way.
//
// Adding a tracer needs an API key. A new install has its setup key; an
// install from before this is given its tracer by a run with --key, install
// or apply again or `inbuxa upgrade`, and is told so until then.

import (
	"context"
	"fmt"
	"os"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/jmap"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

const (
	hostLogDir      = "/var/log/inbuxa"
	containerLogDir = "/var/lib/inbuxa/logs"
)

// LogFilesHint is said to an install that has no log files yet, when there
// is no API key to give it them with.
const LogFilesHint = "the console's Logs page stays empty until the server writes log files, which installs made before installer #70 do not: " +
	"run `inbuxa upgrade --key -` once, with an API key made in the console, to turn them on"

// logDirs is where the server's log files go in a shape, and where they go
// in the other one.
func logDirs(onHost bool) (dir, other string) {
	if onHost {
		return hostLogDir, containerLogDir
	}
	return containerLogDir, hostLogDir
}

// turnOnLogFiles makes the log directory and has the server write its log
// files there. moved says the server has just moved between a container and
// the host, so the installer's tracer may still name the other shape's
// directory, which is then corrected.
func turnOnLogFiles(ctx context.Context, srv *jmap.Client, onHost, moved bool, cmp docker.Compose, log Log) error {
	dir, other := logDirs(onHost)
	if onHost {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if err := runQuiet(ctx, "chown", HostServerUser+":"+HostServerUser, dir); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o750); err != nil {
			return err
		}
	} else if err := cmp.Run(ctx, "exec", "-T", "server", "mkdir", "-p", "-m", "0750", dir); err != nil {
		return fmt.Errorf("making %s in the server's container: %w", dir, err)
	}
	var replaces []string
	if moved {
		replaces = []string{other}
	}
	change, err := srv.EnsureLogTracer(ctx, dir, replaces...)
	if err != nil {
		return err
	}
	switch change {
	case jmap.LogTracerPresent:
		log.Info("log files in %s already", dir)
	case jmap.LogTracerAdded:
		log.Info("log files in %s, one a day, for the console's Logs page", dir)
	case jmap.LogTracerMoved:
		log.Info("the server's log files are in %s now", dir)
	case jmap.LogTracerKept:
		log.Info("a log file tracer set up in the console is kept as it is")
	}
	return nil
}

// LogFilesDue is whether the server installed here has not been given its
// log files yet.
func LogFilesDue(st *state.State) bool {
	sh := plan.Shape(st.Shapes[string(plan.Server)])
	return (sh == plan.Host || sh == plan.Container) && !st.Removed() && !st.LogFiles
}

// LogFiles gives an installed server its log files, with an API key, and
// records that it has them.
func LogFiles(ctx context.Context, st *state.State, key string, f host.Facts, log Log) error {
	onHost := plan.Shape(st.Shapes[string(plan.Server)]) == plan.Host
	docker.UseRuntime(f.Runtime)
	cmp := docker.Compose{Dir: st.Dir, Runtime: f.Runtime}
	log.Step("turning on the server's log files")
	srv := &jmap.Client{BaseURL: "http://" + hostFinalBind, Token: key}
	if err := turnOnLogFiles(ctx, srv, onHost, false, cmp, log); err != nil {
		return err
	}
	st.LogFiles = true
	if err := st.Save(); err != nil {
		log.Info("could not write %s: %v", state.Path, err)
	}
	return nil
}
