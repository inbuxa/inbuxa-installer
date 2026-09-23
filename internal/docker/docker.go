// SPDX-FileCopyrightText: 2026 Coffey Labs
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package docker drives the docker CLI. The CLI rather than the Engine API,
// because compose is the thing being driven and the CLI is how it ships: the
// deployment the tool leaves behind is one an operator manages with the same
// commands.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Output runs docker with args and returns its standard output.
func Output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Pull pulls one image. Quiet, because it runs before the plan is confirmed
// and the step already says what it is fetching.
func Pull(ctx context.Context, image string) error {
	_, err := Output(ctx, "pull", "--quiet", image)
	return err
}

// ImageID is the local ID of an image, which is the same for two references
// only when they are the same image.
func ImageID(ctx context.Context, image string) (string, error) {
	return Output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
}

// ImageEnv is the value an image's configuration gives an environment
// variable, or "" when it sets none.
func ImageEnv(ctx context.Context, image, name string) (string, error) {
	out, err := Output(ctx, "image", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", image)
	if err != nil {
		return "", err
	}
	return envValue(out, name), nil
}

func envValue(env, name string) string {
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			return v
		}
	}
	return ""
}

// RepoDigest is an image's by-digest reference in one repository, e.g.
// registry.coffeylabs.org/coffey-labs/ihasmail@sha256:..., which names exactly that image for
// as long as the registry keeps it.
func RepoDigest(ctx context.Context, image, repository string) (string, error) {
	out, err := Output(ctx, "image", "inspect", "--format", "{{range .RepoDigests}}{{println .}}{{end}}", image)
	if err != nil {
		return "", err
	}
	for _, d := range strings.Fields(out) {
		if strings.HasPrefix(d, repository+"@") {
			return d, nil
		}
	}
	return "", fmt.Errorf("%s has no digest from %s", image, repository)
}

// Versions returns the engine and compose versions, which is also the check
// that both are installed and this user may use them.
func Versions(ctx context.Context) (engine, compose string, err error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", "", errors.New("docker is not installed, or not on PATH")
	}
	if engine, err = Output(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return "", "", fmt.Errorf("cannot talk to the Docker daemon -- is it running, and may this user use it? (%w)", err)
	}
	if compose, err = Output(ctx, "compose", "version", "--short"); err != nil {
		return engine, "", fmt.Errorf("the docker compose plugin is not installed (%w)", err)
	}
	return engine, compose, nil
}

// ProjectLeftovers lists containers, volumes and networks already labeled
// with a compose project name. Any at all means an earlier run of the same
// project, and its volumes would hand a "fresh" deployment an old server.
func ProjectLeftovers(ctx context.Context, project string) ([]string, error) {
	filter := "label=com.docker.compose.project=" + project
	var found []string
	for _, kind := range []struct{ name, format string }{
		{"container", "{{.Names}}"},
		{"volume", "{{.Name}}"},
		{"network", "{{.Name}}"},
	} {
		args := []string{kind.name, "ls", "--filter", filter, "--format", kind.format}
		if kind.name == "container" {
			args = []string{"ps", "-a", "--filter", filter, "--format", kind.format}
		}
		out, err := Output(ctx, args...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Fields(out) {
			found = append(found, kind.name+" "+line)
		}
	}
	return found, nil
}

// Compose runs docker compose against one deployment directory.
type Compose struct {
	Dir   string
	Files []string // extra -f files after compose.yaml, e.g. the bootstrap override
	Env   []string // added to the environment compose interpolates from
	Out   io.Writer
}

// Run runs a compose command with its output passed through: pulling images
// takes long enough that silence would look like a hang. Everything but a pull
// is quiet, because without a terminal compose prints each container's every
// state change twice and the tool already says what step it is on.
func (c Compose) Run(ctx context.Context, args ...string) error {
	full := []string{"compose", "--project-directory", c.Dir, "-f", c.Dir + "/compose.yaml"}
	if len(args) > 0 && args[0] != "pull" {
		full = append(full, "--progress", "quiet")
	}
	for _, f := range c.Files {
		full = append(full, "-f", f)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdout, cmd.Stderr = c.Out, c.Out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Running reports whether a service has a running container.
func (c Compose) Running(ctx context.Context, service string) bool {
	out, err := Output(ctx, "compose", "--project-directory", c.Dir, "-f", c.Dir+"/compose.yaml",
		"ps", "--status", "running", "--services")
	if err != nil {
		return false
	}
	for _, s := range strings.Fields(out) {
		if s == service {
			return true
		}
	}
	return false
}

// Logs returns the last lines of one service's log, for a failure report.
func (c Compose) Logs(ctx context.Context, service string, lines int) string {
	out, err := Output(ctx, "compose", "--project-directory", c.Dir, "-f", c.Dir+"/compose.yaml",
		"logs", "--no-color", "--tail", fmt.Sprint(lines), service)
	if err != nil {
		return err.Error()
	}
	return out
}

// SystemCABundle reads the CA bundle out of an image, so a private CA can be
// added to the roots the image already trusts rather than replacing them.
func SystemCABundle(ctx context.Context, image string) ([]byte, error) {
	out, err := Output(ctx, "run", "--rm", "--entrypoint", "cat", image, "/etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		return nil, err
	}
	return []byte(out + "\n"), nil
}
