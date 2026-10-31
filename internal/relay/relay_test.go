// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package relay

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"git.coffeylabs.org/inbuxa/inbuxa-installer/internal/release"
)

// The relay's own test of a signature made with openssl, as
// tools/release/sign-relay.sh makes them: the same bytes pass here.
func TestOpenSSLSignature(t *testing.T) {
	keys, err := ParseKeys([]string{"VNaWnerWh2UXOrUMtSWPOKJHtigsUdIWUfzHmXbdgIU"})
	if err != nil {
		t.Fatal(err)
	}
	file := "inbuxa-relay release 1\n" +
		"version 27.1.0-rc.5\n" +
		"target x86_64-linux-musl\n" +
		"sha256 ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\n" +
		"signature zRj90ByVN18fmW-GOZ9cvGBK1ZsYYoNvowGsbSBYcslJhgIgmKrYHgRgq1CSIy4G-EzR5uexCzAUoa3FEcluAA\n"
	if err := Verify([]byte("abc"), file, keys, "27.1.0-rc.5", "x86_64-linux-musl"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ binary, version, target, want string }{
		{"abd", "27.1.0-rc.5", "x86_64-linux-musl", "another binary"},
		{"abc", "27.1.0-rc.4", "x86_64-linux-musl", "release 27.1.0-rc.5, not 27.1.0-rc.4"},
		{"abc", "27.1.0-rc.5", "aarch64-linux-musl", "for x86_64-linux-musl"},
	} {
		err := Verify([]byte(c.binary), file, keys, c.version, c.target)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestSignatureFiles(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	other, _, _ := ed25519.GenerateKey(nil)
	binary := []byte("a relay")
	sign := func(version string) string {
		text := fmt.Sprintf("inbuxa-relay release 1\nversion %s\ntarget x86_64-linux-musl\nsha256 %x\n", version, sha256.Sum256(binary))
		return text + "signature " + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(text))) + "\n"
	}
	good := sign("27.1.0-rc.5")
	if err := Verify(binary, good, []ed25519.PublicKey{other, pub}, "27.1.0-rc.5", "x86_64-linux-musl"); err != nil {
		t.Fatalf("signed by the second key: %v", err)
	}
	if err := Verify(binary, strings.ReplaceAll(good, "\n", "\r\n"), []ed25519.PublicKey{pub}, "27.1.0-rc.5", "x86_64-linux-musl"); err != nil {
		t.Fatalf("CRLF: %v", err)
	}
	for name, c := range map[string]struct {
		file string
		keys []ed25519.PublicKey
		want string
	}{
		"other key":   {good, []ed25519.PublicKey{other}, "no inbuxa release key"},
		"no key":      {good, nil, "no release key"},
		"changed":     {strings.Replace(good, "rc.5", "rc.6", 1), []ed25519.PublicKey{pub}, "no inbuxa release key"},
		"not one":     {"hello\n", []ed25519.PublicKey{pub}, "doesn't start"},
		"short":       {"inbuxa-relay release 1\nversion 27.1.0\n", []ed25519.PublicKey{pub}, "no target"},
		"more":        {good + "extra\n", []ed25519.PublicKey{pub}, "goes on past"},
		"bad version": {sign("27.1"), []ed25519.PublicKey{pub}, "not a version"},
	} {
		if _, err := VerifySignature(c.file, c.keys); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The keys the installer checks with are the relay's, decodable.
func TestReleaseKeys(t *testing.T) {
	keys, err := ParseKeys(release.RelayKeys)
	if err != nil || len(keys) != 2 {
		t.Fatalf("%v %v", keys, err)
	}
	if keys[0][0] != 0x27 || keys[1][0] != 0xbc || keys[0][31] != 0xec || keys[1][31] != 0xff {
		t.Errorf("not the bytes release.rs holds")
	}
}

func TestVersionOrder(t *testing.T) {
	ordered := []string{"27.0.9", "27.1.0-alpha", "27.1.0-rc.2", "27.1.0-rc.10", "27.1.0-rc.10.1", "27.1.0-rcx", "27.1.0", "27.1.1", "28.0.0"}
	for i := range ordered {
		for j := range ordered {
			c, ok := compareVersions(ordered[i], ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if !ok || c != want {
				t.Errorf("%s vs %s: %d %v, want %d", ordered[i], ordered[j], c, ok, want)
			}
		}
	}
	if c, ok := compareVersions("27.1.0+build", "27.1.0"); !ok || c != 0 {
		t.Errorf("build metadata counts: %d %v", c, ok)
	}
	for _, bad := range []string{"27.1", "27.1.x", "27.1.0-", "27.1.0-a..b", ""} {
		if _, ok := compareVersions(bad, "27.1.0"); ok {
			t.Errorf("%q is a version", bad)
		}
	}
}

func TestCargoVersion(t *testing.T) {
	for in, want := range map[string]string{
		"27.1-rc.4": "27.1.0-rc.4", "27.1": "27.1.0", "27.1.3": "27.1.3", "27.1.3-rc.1": "27.1.3-rc.1",
	} {
		if got := release.CargoVersion(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestVersionOutput(t *testing.T) {
	for out, want := range map[string]string{
		"inbuxa-relay 27.1.0-rc.5\n": "27.1.0-rc.5",
		"inbuxa 27.1-rc.4\n":         "",
		"error while loading":        "",
		"":                           "",
	} {
		if got := parseVersionOutput(out); got != want {
			t.Errorf("%q: %q", out, got)
		}
	}
}

func TestUnitLinkListen(t *testing.T) {
	plain := unitFile(DefaultLinkListen)
	if strings.Contains(plain, "--link-listen") {
		t.Error("the default needs no --link-listen")
	}
	if unitLinkListen(plain) != "" {
		t.Error("a plain unit names a link address")
	}
	wg := unitFile("[fd42:1::2]:443")
	if !strings.Contains(wg, "ExecStart="+Binary+" run --state "+StateDir+" --link-listen [fd42:1::2]:443\n") {
		t.Errorf("ExecStart: %s", wg)
	}
	if got := unitLinkListen(wg); got != "[fd42:1::2]:443" {
		t.Errorf("read back %q", got)
	}
	for _, want := range []string{"User=inbuxa-relay", "ReadWritePaths=/opt/inbuxa-relay/bin", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE", "Restart=always"} {
		if !strings.Contains(plain, want+"\n") {
			t.Errorf("no %s", want)
		}
	}
}
