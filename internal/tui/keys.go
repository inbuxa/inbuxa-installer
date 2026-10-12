// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import "unicode/utf8"

// KeyKind is what a key press was, once its bytes are decoded.
type KeyKind int

const (
	KeyRune KeyKind = iota // a character, in Key.Rune
	KeyEnter
	KeyEsc
	KeyTab
	KeyBackTab
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyCtrlC
	KeyCtrlU // clear the field
)

// Key is one key press.
type Key struct {
	Kind KeyKind
	Rune rune
}

// decode turns what one read from the terminal returned into key presses. A
// terminal sends an escape sequence in one write, so one read holds all of
// it; an Esc on its own is the Esc key.
func decode(b []byte) []Key {
	var keys []Key
	for len(b) > 0 {
		k, n := decodeOne(b)
		if n == 0 {
			break
		}
		if k != nil {
			keys = append(keys, *k)
		}
		b = b[n:]
	}
	return keys
}

var csi = map[string]KeyKind{
	"A": KeyUp, "B": KeyDown, "C": KeyRight, "D": KeyLeft,
	"H": KeyHome, "F": KeyEnd, "Z": KeyBackTab,
	"1~": KeyHome, "7~": KeyHome, "4~": KeyEnd, "8~": KeyEnd,
	"3~": KeyDelete, "5~": KeyPgUp, "6~": KeyPgDn,
}

func decodeOne(b []byte) (*Key, int) {
	key := func(k KeyKind) *Key { return &Key{Kind: k} }
	switch c := b[0]; {
	case c == 0x1b:
		if len(b) == 1 {
			return key(KeyEsc), 1
		}
		if b[1] == '[' || b[1] == 'O' {
			// CSI or SS3: parameters, then one final byte in @..~
			for i := 2; i < len(b); i++ {
				if b[i] >= 0x40 && b[i] <= 0x7e {
					if k, ok := csi[string(b[2:i+1])]; ok {
						return key(k), i + 1
					}
					return nil, i + 1 // a sequence this does not use
				}
			}
			return nil, len(b)
		}
		return key(KeyEsc), 1
	case c == 0x03:
		return key(KeyCtrlC), 1
	case c == 0x15:
		return key(KeyCtrlU), 1
	case c == 0x01:
		return key(KeyHome), 1
	case c == 0x05:
		return key(KeyEnd), 1
	case c == '\r' || c == '\n':
		return key(KeyEnter), 1
	case c == '\t':
		return key(KeyTab), 1
	case c == 0x7f || c == 0x08:
		return key(KeyBackspace), 1
	case c < 0x20:
		return nil, 1 // another control character: ignored
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError && n <= 1 {
		return nil, 1
	}
	return &Key{Kind: KeyRune, Rune: r}, n
}
