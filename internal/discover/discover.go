// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package discover reads what is actually on this machine, as opposed to
// what the installer wrote down that it did.
//
// The two are not the same thing, and the difference is the useful part. A
// container stopped by hand, a deployment directory copied from another
// machine, an installation made by an older version of this program, or a
// service someone removed with the runtime directly: in each case intent and
// reality have parted company, and an operator wants to be told, not to have
// one quietly reported as the other.
//
// So: state is intent, this is reality, and the two are shown side by side.
package discover

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
)

// Found is what this machine turns out to be running.
type Found struct {
	Dir        string            // the deployment directory, when there is one
	Domain     string            // read back from the deployment
	MailHost   string            //
	Services   map[string]string // component -> running|stopped|absent
	Compose    []string          // services the compose file declares
	Runtime    string            // what is running them
	Proxy      bool              // the deployment includes a proxy
	Credential bool              // credentials.txt is there
}

// Dirs are where a deployment may be. The first is what this installer
// writes; the second is oneshot's habit, and someone moving across will have
// one.
var Dirs = []string{"/var/lib/inbuxa", "/opt/inbuxa"}

var serviceLine = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_-]*):\s*$`)
var hostnameLine = regexp.MustCompile(`(?m)^\s+hostname:\s*(\S+)\s*$`)

// Run looks for a deployment and reports what it finds. It never changes
// anything, and a machine with nothing on it is not an error.
func Run(ctx context.Context, f host.Facts, dirs ...string) Found {
	found := Found{Services: map[string]string{}, Runtime: f.Runtime.Kind}
	if len(dirs) == 0 {
		dirs = Dirs
	}
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err == nil {
			found.Dir = dir
			break
		}
	}
	if found.Dir == "" {
		return found
	}

	if _, err := os.Stat(filepath.Join(found.Dir, "credentials.txt")); err == nil {
		found.Credential = true
	}

	b, err := os.ReadFile(filepath.Join(found.Dir, "compose.yaml"))
	if err != nil {
		return found
	}
	text := string(b)
	for _, m := range serviceLine.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if name == "caddy" {
			found.Proxy = true
			continue
		}
		if name == "server" || name == "console" || name == "webmail" {
			found.Compose = append(found.Compose, name)
			found.Services[name] = "stopped"
		}
	}
	// The mail host is written into the server's hostname, and the domain
	// follows from it. Read back rather than assumed, because this may be a
	// deployment this program did not write.
	if m := hostnameLine.FindStringSubmatch(text); m != nil {
		found.MailHost = m[1]
		if _, rest, ok := strings.Cut(m[1], "."); ok {
			found.Domain = rest
		}
	}

	// What is actually up. A compose file that lists a service proves
	// nothing about whether it is running.
	if f.Runtime.Usable {
		docker.UseRuntime(f.Runtime)
		cmp := docker.Compose{Dir: found.Dir, Runtime: f.Runtime}
		for _, name := range found.Compose {
			if cmp.Running(ctx, name) {
				found.Services[name] = "running"
			}
		}
	}
	return found
}

// Components is what a topology file would say this machine runs. Everything
// the deployment declares counts, running or not: a stopped webmail is still
// installed here, and a file that left it out would be asking for it to be
// removed.
func (f Found) Components() []string {
	var out []string
	for _, name := range []string{"server", "console", "webmail"} {
		if _, ok := f.Services[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// Drift is where intent and reality disagree, in sentences fit to show
// someone. Empty means they agree.
func Drift(shapes map[string]string, f Found) []string {
	var out []string
	for _, kind := range []string{"server", "console", "webmail"} {
		_, intended := shapes[kind]
		state, present := f.Services[kind]
		switch {
		case intended && !present:
			out = append(out, kind+" was installed here, and the deployment no longer has it")
		case !intended && present:
			out = append(out, kind+" is in the deployment, and nothing recorded installing it")
		case intended && present && state != "running":
			out = append(out, kind+" is installed and not running")
		}
	}
	return out
}
