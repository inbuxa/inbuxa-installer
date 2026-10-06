// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package state is what this machine remembers about what was installed on
// it, so a second run converges instead of building a second one.
//
// It is deliberately small, and deliberately not the truth: the truth is the
// machine, and a plan is built by reading both. What this adds is intent --
// which components this installer put here, in which shape, from which
// topology -- which the machine itself cannot tell you. A container that was
// stopped by hand is still ours; a container we never installed is not.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Path is where it lives. /etc, not the deployment directory: it survives
// the deployment directory being moved or rebuilt, and an operator looking
// for "what did this thing do to my machine" looks in /etc.
const Path = "/etc/inbuxa/install.json"

// State is the file.
type State struct {
	Version   int               `json:"version"`
	Machine   string            `json:"machine"`             // its name in the topology
	Dir       string            `json:"dir"`                 // the deployment directory
	Runtime   string            `json:"runtime,omitempty"`   // docker or podman
	Domain    string            `json:"domain,omitempty"`    //
	Shapes    map[string]string `json:"shapes"`              // component -> container|host
	Topology  string            `json:"topology,omitempty"`  // the file this came from, if any
	Updated   time.Time         `json:"updated"`             //
	Installer string            `json:"installer,omitempty"` // the version that wrote this
}

const Version = 1

// Load reads the state, returning an empty one when there is none. A machine
// with nothing installed is not an error; it is the ordinary first case.
func Load() (*State, error) {
	b, err := os.ReadFile(Path)
	if os.IsNotExist(err) {
		return &State{Version: Version, Shapes: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Shapes == nil {
		s.Shapes = map[string]string{}
	}
	return &s, nil
}

// Save writes it, creating /etc/inbuxa if it is not there.
func (s *State) Save() error {
	s.Version = Version
	s.Updated = time.Now().UTC().Truncate(time.Second)
	if err := os.MkdirAll(filepath.Dir(Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path, append(b, '\n'), 0o644)
}

// Installed is whether anything is recorded here at all.
func (s *State) Installed() bool { return len(s.Shapes) > 0 }
