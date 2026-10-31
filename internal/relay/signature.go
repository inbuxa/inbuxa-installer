// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package relay

// A relay binary's .sig file, checked as the relay checks one before an
// upgrade (crates/relay-proto/src/release.rs): five lines, the first four
// signed with Ed25519 by an inbuxa release key.
//
//	inbuxa-relay release 1
//	version 27.1.0-rc.5
//	target x86_64-linux-musl
//	sha256 <hex>
//	signature <base64url>
//
// The pinned checksum already says the download is the release's; the
// signature says the release is inbuxa's, the same check the relay makes of
// every binary the console sends it.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const sigHeader = "inbuxa-relay release 1"

// Signed is what a signature file says, once a key is found to have signed
// it.
type Signed struct {
	Version, Target string
	SHA256          [32]byte
}

// ParseKeys decodes base64url public keys.
func ParseKeys(keys []string) ([]ed25519.PublicKey, error) {
	var out []ed25519.PublicKey
	for _, k := range keys {
		b, err := base64.RawURLEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("release key %q is not an Ed25519 public key", k)
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}

// VerifySignature reads a signature file and checks it against keys, but not
// the binary.
func VerifySignature(file string, keys []ed25519.PublicKey) (Signed, error) {
	var s Signed
	lines := strings.Split(strings.ReplaceAll(file, "\r\n", "\n"), "\n")
	if lines[0] != sigHeader {
		return s, errors.New("the signature file doesn't start as one")
	}
	rest := lines[1:]
	field := func(name string) (string, error) {
		if len(rest) == 0 {
			return "", fmt.Errorf("the signature file has no %s", name)
		}
		v, ok := strings.CutPrefix(rest[0], name+" ")
		rest = rest[1:]
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			return "", fmt.Errorf("the signature file has no %s", name)
		}
		return v, nil
	}
	var err error
	if s.Version, err = field("version"); err != nil {
		return s, err
	}
	if s.Target, err = field("target"); err != nil {
		return s, err
	}
	sum, err := field("sha256")
	if err != nil {
		return s, err
	}
	if b, err := hex.DecodeString(sum); err != nil || len(b) != 32 {
		return s, errors.New("the signature file's sha256 is not a SHA-256")
	} else {
		copy(s.SHA256[:], b)
	}
	sigText, err := field("signature")
	if err != nil {
		return s, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sigText, "="))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return s, errors.New("the signature file's signature is not an Ed25519 signature")
	}
	if _, ok := parseVersion(s.Version); !ok {
		return s, fmt.Errorf("the signature file's version %q is not a version", s.Version)
	}
	for _, l := range rest {
		if strings.TrimSpace(l) != "" {
			return s, errors.New("the signature file goes on past the signature")
		}
	}
	if len(keys) == 0 {
		return s, errors.New("there is no release key to check the signature with")
	}
	signed := fmt.Sprintf("%s\nversion %s\ntarget %s\nsha256 %x\n", sigHeader, s.Version, s.Target, s.SHA256)
	for _, k := range keys {
		if ed25519.Verify(k, []byte(signed), sig) {
			return s, nil
		}
	}
	return s, errors.New("no inbuxa release key signed it")
}

// Verify checks a binary against its signature file, and that the file is
// for the release and the architecture wanted.
func Verify(binary []byte, file string, keys []ed25519.PublicKey, version, target string) error {
	s, err := VerifySignature(file, keys)
	if err != nil {
		return err
	}
	if sha256.Sum256(binary) != s.SHA256 {
		return errors.New("the signature is for another binary")
	}
	if s.Version != version {
		return fmt.Errorf("the signature is for release %s, not %s", s.Version, version)
	}
	if s.Target != target {
		return fmt.Errorf("the signature is for %s, not %s", s.Target, target)
	}
	return nil
}

// version is MAJOR.MINOR.PATCH[-PRE][+BUILD], ordered as SemVer §11 orders
// them, as the relay orders them.
type version struct {
	core [3]uint64
	pre  []string
}

func parseVersion(text string) (version, bool) {
	var v version
	text, _, _ = strings.Cut(strings.TrimSpace(text), "+")
	core, pre, hasPre := strings.Cut(text, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if id == "" || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
				return v, false
			}
		}
	}
	return v, true
}

// compareVersions is -1, 0 or 1 as a is before, the same as, or after b.
// ok is false when either is not a version.
func compareVersions(a, b string) (c int, ok bool) {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range va.core {
		if va.core[i] != vb.core[i] {
			return cmp(va.core[i] < vb.core[i]), true
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, true
	case len(va.pre) == 0:
		return 1, true
	case len(vb.pre) == 0:
		return -1, true
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		x, y := va.pre[i], vb.pre[i]
		if x == y {
			continue
		}
		nx, ex := strconv.ParseUint(x, 10, 64)
		ny, ey := strconv.ParseUint(y, 10, 64)
		switch {
		case ex == nil && ey == nil:
			return cmp(nx < ny), true
		case ex == nil: // numeric identifiers come first
			return -1, true
		case ey == nil:
			return 1, true
		}
		return cmp(x < y), true
	}
	if len(va.pre) == len(vb.pre) {
		return 0, true
	}
	return cmp(len(va.pre) < len(vb.pre)), true
}

func cmp(less bool) int {
	if less {
		return -1
	}
	return 1
}
