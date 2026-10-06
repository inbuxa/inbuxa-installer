// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Installs made before issue #5 was fixed mounted the server's named volumes
// at /opt/stalwart/etc and /opt/stalwart/data, which the server never reads.
// Its configuration and mail went into the anonymous volumes its image
// declares at /etc/inbuxa and /var/lib/inbuxa instead. Recreating the server
// with the corrected mounts would put the empty named volumes over those
// paths and bring it back in bootstrap mode, so the data is moved first.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
)

// serverVolumes is where each of the server's named volumes belongs.
var serverVolumes = map[string]string{
	"/etc/inbuxa":     "inbuxa-etc",
	"/var/lib/inbuxa": "inbuxa-data",
}

type volumeMove struct {
	dest, from, to string
}

// serverVolumeMoves reads the server container's mounts and returns the data
// that sits in a volume other than the named one meant for its path. None
// means the server is already on its named volumes, or there is no server.
func serverVolumeMoves(ctx context.Context, cmp docker.Compose, project string) (moves []volumeMove, image string, err error) {
	id, err := cmp.Output(ctx, "--project-directory", cmp.Dir, "-f", cmp.Dir+"/compose.yaml", "ps", "-a", "-q", "server")
	if err != nil || id == "" {
		return nil, "", err
	}
	id = strings.Fields(id)[0]
	raw, err := docker.Output(ctx, "inspect", "--format", "{{json .Mounts}} {{.Config.Image}}", id)
	if err != nil {
		return nil, "", err
	}
	mountsJSON, image, _ := strings.Cut(raw, " ")
	var mounts []struct {
		Type, Name, Destination string
	}
	if err := json.Unmarshal([]byte(mountsJSON), &mounts); err != nil {
		return nil, "", fmt.Errorf("reading the server's mounts: %w", err)
	}
	for _, m := range mounts {
		named, ok := serverVolumes[m.Destination]
		want := project + "_" + named
		if ok && m.Type == "volume" && m.Name != want {
			moves = append(moves, volumeMove{dest: m.Destination, from: m.Name, to: want})
		}
	}
	return moves, strings.TrimSpace(image), nil
}

// adoptServerVolumes moves an older install's server data into its named
// volumes, with the server stopped. It never overwrites: a named volume that
// already holds something stops it, since that is not a state this installer
// leaves and guessing which copy is the real one could lose mail. The old
// anonymous volumes are left in place, so the move can be undone.
func adoptServerVolumes(ctx context.Context, cmp docker.Compose, project string, log Log) (moved bool, err error) {
	moves, image, err := serverVolumeMoves(ctx, cmp, project)
	if err != nil || len(moves) == 0 {
		return false, err
	}
	log.Step("moving the mail server's data onto its named volumes")
	if err := cmp.Run(ctx, "stop", "server"); err != nil {
		return false, err
	}
	// The server's own image, which is already here and has a shell; run as
	// root so cp -a keeps the server user's ownership.
	helper := func(args ...string) (string, error) {
		base := []string{"run", "--rm", "--user", "0", "--entrypoint", "sh"}
		return docker.Output(ctx, append(base, args...)...)
	}
	for _, m := range moves {
		out, err := helper("-v", m.to+":/to", image, "-c", "ls -A /to | head -n 1")
		if err != nil {
			return false, fmt.Errorf("looking in %s: %w", m.to, err)
		}
		if out != "" {
			// `start`, not `up`: the stopped container still has its old
			// mounts, and up would recreate it from the new file.
			restart := ""
			if err := cmp.Run(ctx, "start", "server"); err != nil {
				restart = fmt.Sprintf(" (and starting it again failed: %v)", err)
			}
			return false, fmt.Errorf("the server's data for %s is in %s, but %s already holds something; "+
				"not overwriting it. Nothing was moved, and the server runs as it did%s", m.dest, m.from, m.to, restart)
		}
	}
	for _, m := range moves {
		if _, err := helper("-v", m.from+":/from:ro", "-v", m.to+":/to", image, "-c", "cp -a /from/. /to/"); err != nil {
			return false, fmt.Errorf("copying %s into %s: %w", m.from, m.to, err)
		}
		log.Info("%s: %s -> %s (the old volume is kept)", m.dest, m.from, m.to)
	}
	return true, nil
}
