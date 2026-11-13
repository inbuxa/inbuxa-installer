// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// BackupKind decides how, or whether, Backup would archive this machine's
// mail server. refuse is non-empty when there is nothing here to back up, or
// when a local archive would mislead; otherwise kind is "container" or
// "host". It reads only the state, so the command can say what would happen
// before anything does.
func BackupKind(st *state.State) (kind, refuse string) {
	switch {
	case st.Removed():
		return "", "the programs were uninstalled here and the data kept; nothing is running to back up"
	case st.ServerURL != "":
		return "", "the mail server runs on another machine; it is backed up where it runs, not here"
	case st.Shapes["server"] == "" || strings.EqualFold(st.Shapes["server"], "skip"):
		return "", "the mail server is not installed on this machine"
	case st.Clustered:
		return "", "this node keeps its mail in the cluster's shared store (PostgreSQL and S3), which is backed up there; a local archive would hold only its configuration, so this does not take one"
	case strings.EqualFold(st.Shapes["server"], "host"):
		return "host", ""
	default:
		return "container", ""
	}
}

// Backup archives the mail server's configuration and data on this machine to
// <dir>/backups and returns the archive's path. It stops the server for a
// consistent archive and starts it again -- a brief outage -- so the caller
// should have the operator's go-ahead. It is for a single-machine install;
// BackupKind says when it will not run, and why.
//
// It takes only what lives here: the server's own volumes or its directory. A
// store the server keeps elsewhere -- the cluster's PostgreSQL and S3 -- is
// backed up there, which is why BackupKind refuses on a clustered node rather
// than write a half archive.
func Backup(ctx context.Context, st *state.State, f host.Facts, log Log) (string, error) {
	kind, refuse := BackupKind(st)
	if refuse != "" {
		return "", fmt.Errorf("%s", refuse)
	}
	var (
		name string
		err  error
	)
	if kind == "host" {
		name, err = backupHostServer(ctx, st.Dir, log)
	} else {
		name, err = backupServerContainer(ctx, st.Dir, f, log)
	}
	if err != nil {
		return "", err
	}
	if err := verifyArchive(ctx, name); err != nil {
		return "", fmt.Errorf("the archive was written to %s but did not verify: %w", name, err)
	}
	return name, nil
}

func backupServerContainer(ctx context.Context, dir string, f host.Facts, log Log) (string, error) {
	compose, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		return "", err
	}
	ver := installedVersion(dir, plan.Server, plan.Container, string(compose))
	if ver == "" || ver == "unknown" {
		return "", fmt.Errorf("could not tell the server's version from %s", filepath.Join(dir, "compose.yaml"))
	}
	docker.UseRuntime(f.Runtime)
	cmp := docker.Compose{Dir: dir, Runtime: f.Runtime}
	log.Info("stopping the server to archive its data")
	if err := cmp.Run(ctx, "stop", "server"); err != nil {
		return "", err
	}
	name, archErr := archiveVolumes(ctx, dir, ver)
	// Start it again whatever happened; a backup must not leave it down.
	if startErr := cmp.Run(ctx, "start", "server"); startErr != nil && archErr == nil {
		archErr = fmt.Errorf("the archive %s was written, but starting the server again failed: %w", name, startErr)
	}
	if archErr != nil {
		return "", archErr
	}
	log.Info("archived its configuration and data to %s", name)
	return name, nil
}

func backupHostServer(ctx context.Context, dir string, log Log) (string, error) {
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return "", err
	}
	name := filepath.Join(backups, "server-host-"+time.Now().UTC().Format("20060102T150405Z")+".tar.gz")
	log.Info("stopping the server to archive its data")
	if err := runQuiet(ctx, "systemctl", "stop", HostServerUnit); err != nil {
		return "", err
	}
	tarErr := runQuiet(ctx, "tar", "-czf", name, "-C", dir, "server")
	if startErr := runQuiet(ctx, "systemctl", "start", HostServerUnit); startErr != nil && tarErr == nil {
		tarErr = fmt.Errorf("the archive %s was written, but starting the server again failed: %w", name, startErr)
	}
	if tarErr != nil {
		os.Remove(name)
		return "", tarErr
	}
	log.Info("archived its configuration and data to %s", name)
	return name, nil
}

// verifyArchive confirms a backup is a readable, non-empty gzip tar. A backup
// that is quietly broken is worse than one that loudly failed.
func verifyArchive(ctx context.Context, name string) error {
	fi, err := os.Stat(name)
	if err != nil {
		return err
	}
	if fi.Size() == 0 {
		return fmt.Errorf("it is empty")
	}
	out, err := exec.CommandContext(ctx, "tar", "-tzf", name).Output()
	if err != nil {
		return fmt.Errorf("it is not a readable tar: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("it holds no files")
	}
	return nil
}
