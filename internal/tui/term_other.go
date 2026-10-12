// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package tui

import (
	"errors"
	"os"
	"syscall"
)

// The installer runs on Linux only (main says so before anything else), but
// it compiles everywhere; these keep it compiling.

func rawMode(*os.File) (func(), error) { return nil, errors.New("not a Linux terminal") }
func size(*os.File) (int, int)         { return 80, 24 }

// IsTerminal is false off Linux: the interface is never started there.
func IsTerminal(*os.File) bool { return false }

var resizeSignal os.Signal = syscall.SIGINT
