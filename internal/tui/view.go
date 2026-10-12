// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"os"
	"strings"
	"unicode/utf8"
)

// Styles are SGR sequences around text. Colour is dropped when NO_COLOR is
// set (no-color.org); bold, dim and reverse stay, because they carry
// meaning -- what is selected, what can't be chosen -- that the text alone
// would not.
var noColor = os.Getenv("NO_COLOR") != ""

func sgr(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }
func colour(code, s string) string {
	if noColor {
		return s
	}
	return sgr(code, s)
}

func Bold(s string) string    { return sgr("1", s) }
func Dim(s string) string     { return sgr("2", s) }
func Reverse(s string) string { return sgr("7", s) }
func Accent(s string) string  { return colour("36", s) }
func Good(s string) string    { return colour("32", s) }
func Bad(s string) string     { return colour("31", s) }
func Warn(s string) string    { return colour("33", s) }

// Width is how many cells a string takes, escape sequences not counted. The
// interface's own text is ASCII and Latin; wide characters count as one,
// which only ever costs a cell of padding.
func Width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

func skipEscape(s string, i int) int {
	j := i + 1
	if j < len(s) && s[j] == '[' {
		j++
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
	}
	return j + 1
}

// Fit cuts a line to w cells, keeping its escape sequences whole and ending
// with an ellipsis when something was cut.
func Fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := skipEscape(s, i)
			b.WriteString(s[i:min(j, len(s))])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if n == w-1 {
			b.WriteString("…")
			break
		}
		b.WriteString(s[i : i+size])
		i += size
		n++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// Wrap breaks plain text into lines of at most w cells, at spaces where it
// can, with continuation lines hung two cells further in, which keeps a
// plan's detail lines readable as one item each.
func Wrap(s string, w int) []string { return wrap(s, w, 2) }

// WrapProse is Wrap for running text: continuation lines start where the
// first did.
func WrapProse(s string, w int) []string { return wrap(s, w, 0) }

func wrap(s string, w, hang int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		indent := len(para) - len(strings.TrimLeft(para, " "))
		if Width(para) <= w {
			out = append(out, para)
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = strings.Repeat(" ", indent) + word
			case Width(line)+1+Width(word) > w:
				out = append(out, line)
				line = strings.Repeat(" ", indent+hang) + word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

// Pad fills a line out to w cells.
func Pad(s string, w int) string {
	if n := Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}
