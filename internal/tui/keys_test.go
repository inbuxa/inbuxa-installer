// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"reflect"
	"testing"
)

func TestDecode(t *testing.T) {
	k := func(kind KeyKind) Key { return Key{Kind: kind} }
	r := func(c rune) Key { return Key{Kind: KeyRune, Rune: c} }
	for _, tc := range []struct {
		in   string
		want []Key
	}{
		{"a", []Key{r('a')}},
		{"é", []Key{r('é')}},
		{"\r", []Key{k(KeyEnter)}},
		{"\x1b", []Key{k(KeyEsc)}},
		{"\x1b[A\x1b[B\x1b[C\x1b[D", []Key{k(KeyUp), k(KeyDown), k(KeyRight), k(KeyLeft)}},
		{"\x1bOA", []Key{k(KeyUp)}},
		{"\x1b[Z", []Key{k(KeyBackTab)}},
		{"\x1b[3~\x1b[5~\x1b[6~", []Key{k(KeyDelete), k(KeyPgUp), k(KeyPgDn)}},
		{"\x1b[1;5C", nil}, // Ctrl-Right: not used, swallowed whole
		{"ab\x7f", []Key{r('a'), r('b'), k(KeyBackspace)}},
		{"\x03", []Key{k(KeyCtrlC)}},
		{"\x1bx", []Key{k(KeyEsc), r('x')}},
	} {
		if got := decode([]byte(tc.in)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("decode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFitKeepsEscapesWhole(t *testing.T) {
	s := Bold("abcdef") + "ghij"
	if got := Width(Fit(s, 5)); got != 5 {
		t.Errorf("Fit to 5 is %d wide: %q", got, Fit(s, 5))
	}
	if Fit("abc", 5) != "abc" {
		t.Error("a short line was changed")
	}
	if w := Wrap("  one two three four five six", 12); len(w) < 2 || w[1][:4] != "    " {
		t.Errorf("wrap = %q", w)
	}
}
