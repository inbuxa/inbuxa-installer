// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

// Model is what the terminal shows and what keys do to it.
type Model interface {
	// Update handles a key and says whether the program should end.
	Update(Key) (quit bool)
	// View is the whole screen, as lines; Run fits them to the window.
	View(w, h int) []string
}

// Ticker is a Model that changes on its own -- an install making progress --
// and says so on the channel, so the screen is redrawn without a key.
type Ticker interface {
	Changed() <-chan struct{}
}

const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
)

// Run gives the terminal to m until m says to quit. The terminal is put back
// as it was however Run ends, a panic included: a mail-server installer that
// leaves a terminal in raw mode has failed at the one thing it was trusted
// with first.
func Run(m Model, in, out *os.File) (err error) {
	restore, err := rawMode(in)
	if err != nil {
		return fmt.Errorf("the terminal would not switch to raw mode: %w", err)
	}
	io.WriteString(out, altScreenOn+cursorHide)
	defer func() {
		io.WriteString(out, "\x1b[0m"+cursorShow+altScreenOff)
		restore()
		if r := recover(); r != nil {
			err = fmt.Errorf("the interface stopped: %v", r)
		}
	}()

	keys := make(chan []Key)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if err != nil {
				close(keys)
				return
			}
			keys <- decode(buf[:n])
		}
	}()
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, resizeSignal)
	defer signal.Stop(resized)
	var changed <-chan struct{}
	if t, ok := m.(Ticker); ok {
		changed = t.Changed()
	}

	for {
		draw(m, out)
		select {
		case ks, ok := <-keys:
			if !ok {
				return nil
			}
			for _, k := range ks {
				if m.Update(k) {
					return nil
				}
			}
		case <-resized:
		case <-changed:
		}
	}
}

// draw paints the whole frame. Every line is rewritten and the rest of each
// line cleared, which is plenty for six screens of text and leaves no
// half-drawn state to get wrong.
func draw(m Model, out io.Writer) {
	w, h := size(os.Stdout)
	lines := m.View(w, h)
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i := 0; i < h; i++ {
		if i < len(lines) {
			b.WriteString(Fit(lines[i], w))
		}
		b.WriteString("\x1b[0m\x1b[K")
		if i < h-1 {
			b.WriteString("\r\n")
		}
	}
	io.WriteString(out, b.String())
}
