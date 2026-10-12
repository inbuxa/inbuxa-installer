// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

// Uninstall takes away what the installer put on this machine.
//
// The programs go: containers and the compose project, units, binaries and
// the old ones kept for rollback, the console's and the webmail's files, the
// webmail's user, the installer's own Node. The data stays unless --purge:
// the server's volumes or its directory, Caddy's certificates, the
// credentials, the secrets and the zone file -- and the install record,
// marked removed, so `install` run again picks all of it up.
//
// Never touched, purge or not: the backups the installer made (named, for
// the operator to delete once sure), Docker and anything else the
// distribution installed, and the operator's own web server -- if it loads
// the proxy configuration that was written for it, taking that file away
// would break its next reload, so it is said instead.
//
// Taking one component off is `install` with that component skipped, which
// converges; this is the whole machine.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/docker"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/host"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/plan"
	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/state"
)

// Removal is what uninstall would do, in the words it is shown in.
type Removal struct {
	Remove []string
	Keep   []string
	Notes  []string
}

// UninstallPlan says what goes and what stays, for what is recorded here.
func UninstallPlan(st *state.State, purge bool) Removal {
	var r Removal
	dir := st.Dir
	keepOrRemove := func(what string) {
		if purge {
			r.Remove = append(r.Remove, what)
		} else {
			r.Keep = append(r.Keep, what)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	if exists(filepath.Join(dir, "compose.yaml")) {
		r.Remove = append(r.Remove, "the containers of the compose project in "+dir)
		if st.Shapes["server"] == string(plan.Container) {
			keepOrRemove("the server's volumes, inbuxa_inbuxa-etc and inbuxa_inbuxa-data (its configuration and mail)")
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); strings.Contains(string(b), "\n  caddy:\n") {
			keepOrRemove("Caddy's volumes (its certificates and ACME account)")
		}
	}
	if st.Shapes["server"] == string(plan.Host) {
		r.Remove = append(r.Remove, HostServerUnit+", "+HostServerBinary+" and any older binaries kept beside it")
		keepOrRemove(filepath.Join(dir, "server") + " (the server's configuration and mail) and its user, inbuxa")
	}
	if st.Shapes["console"] == string(plan.Host) {
		r.Remove = append(r.Remove, filepath.Join(dir, "console")+" (the console's files)")
	}
	if st.Shapes["webmail"] == string(plan.Host) {
		r.Remove = append(r.Remove, HostWebmailUnit+", "+filepath.Join(dir, "webmail")+" and its user, "+HostWebmailUser)
		if exists(filepath.Dir(filepath.Dir(host.OwnPath))) {
			r.Remove = append(r.Remove, "the installer's own Node, "+filepath.Dir(filepath.Dir(host.OwnPath)))
		}
	}
	keepOrRemove("credentials.txt, .env, dns.zone and the rest of " + dir)
	if exists(filepath.Join(dir, "proxy")) {
		keepOrRemove(filepath.Join(dir, "proxy") + " (the configuration written for your web server)")
		r.Notes = append(r.Notes, "if your web server loads anything from "+filepath.Join(dir, "proxy")+", take that out of its configuration")
	}
	if exists(filepath.Join(dir, "backups")) {
		r.Keep = append(r.Keep, filepath.Join(dir, "backups")+" (the server's archives: delete it yourself once you are sure)")
	}
	if purge {
		r.Remove = append(r.Remove, "the install record, "+state.Path)
	} else {
		r.Keep = append(r.Keep, "the install record, "+state.Path+", marked removed: `inbuxa install` with the same options brings it all back")
	}
	if st.ServerURL != "" {
		r.Notes = append(r.Notes, "the server at "+st.ServerURL+" still lists these front ends; clear them in its console")
	}
	r.Notes = append(r.Notes, "Docker, and anything else the distribution installed, is left as it is")
	return r
}

// Uninstall carries the plan out.
func Uninstall(ctx context.Context, st *state.State, purge bool, f host.Facts, log Log) error {
	dir := st.Dir
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err == nil {
		log.Step("removing the containers")
		docker.UseRuntime(f.Runtime)
		cmp := docker.Compose{Dir: dir, Runtime: f.Runtime}
		args := []string{"down", "--remove-orphans"}
		if purge {
			args = append(args, "--volumes")
		}
		if err := cmp.Run(ctx, args...); err != nil {
			return err
		}
		os.Remove(filepath.Join(dir, "compose.yaml"))
		os.Remove(filepath.Join(dir, "Caddyfile"))
	}

	if st.Shapes["webmail"] == string(plan.Host) {
		log.Step("removing the webmail from this machine")
		removeUnit(ctx, HostWebmailUnit, webmailUnitPath)
		os.RemoveAll(filepath.Join(dir, "webmail"))
		os.RemoveAll(filepath.Join(dir, "webmail.prev"))
		os.Remove(filepath.Join(dir, "webmail.env"))
		runQuiet(ctx, "userdel", HostWebmailUser)
		os.RemoveAll(filepath.Dir(filepath.Dir(host.OwnPath)))
		os.Remove(filepath.Dir(filepath.Dir(filepath.Dir(host.OwnPath)))) // /opt/inbuxa, if now empty
	}
	if st.Shapes["console"] == string(plan.Host) {
		log.Step("removing the console's files")
		os.RemoveAll(filepath.Join(dir, "console"))
		os.RemoveAll(filepath.Join(dir, "console.prev"))
	}
	if st.Shapes["server"] == string(plan.Host) {
		log.Step("removing the server from this machine")
		removeUnit(ctx, HostServerUnit, hostUnitPath)
		old, _ := filepath.Glob(HostServerBinary + ".*")
		for _, p := range append(old, HostServerBinary) {
			os.Remove(p)
		}
		os.Remove(filepath.Join(dir, "server.env"))
		if purge {
			os.RemoveAll(filepath.Join(dir, "server"))
			runQuiet(ctx, "userdel", HostServerUser)
		}
	}

	if purge {
		log.Step("removing the data (--purge)")
		// By name, as well as by `down --volumes`: after an uninstall that
		// kept them there is no compose file left to name them.
		if f.Runtime.Usable {
			docker.UseRuntime(f.Runtime)
			for _, v := range []string{"inbuxa_inbuxa-etc", "inbuxa_inbuxa-data", "inbuxa_caddy-data", "inbuxa_caddy-config"} {
				docker.Output(ctx, "volume", "rm", "--force", v)
			}
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() == "backups" {
				log.Info("left %s: delete it yourself once you are sure", filepath.Join(dir, "backups"))
				continue
			}
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		os.Remove(dir) // only if nothing is left in it
		os.Remove(state.Path)
		os.Remove(filepath.Dir(state.Path))
		return nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	st.RemovedAt = &now
	if err := st.Save(); err != nil {
		return fmt.Errorf("the programs are gone, but the record of what was kept could not be written: %w", err)
	}
	log.Info("the data is kept; `inbuxa install` with the same options brings it back")
	return nil
}

func removeUnit(ctx context.Context, unit, path string) {
	runQuiet(ctx, "systemctl", "disable", "--now", "--quiet", unit)
	os.Remove(path)
	os.RemoveAll(path + ".d")
	runQuiet(ctx, "systemctl", "daemon-reload")
}
