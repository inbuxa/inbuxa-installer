// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package release is which version of the mail server this installer puts on
// a machine, as an image or as a binary, and what that binary must hash to.
//
// One version for both shapes, so a server moved from a container to the host
// is the same program, and the plan, the image tag and the download cannot
// name three different releases.
//
// A release candidate of the installer (v27.1-rc.1) pins the suite's release
// candidates; a final release pins final releases only. Downloading an RC
// installer is the opt-in: it is published as a pre-release, so
// releases/latest, and so a plain install, never reaches one.
//
// A pin left behind is a fresh install shipped stale. The pin-check command
// (cmd/pin-check, run weekly by .gitea/workflows/pin-check.yml) reads each
// component's releases and reports when the version here is behind the newest
// in its channel, so the move is noticed and made before it ships.
package release

import (
	"fmt"
	"strings"
)

// ServerVersion is the inbuxa-server release this installer installs.
const ServerVersion = "27.1-rc.23"

// ServerImage is that release as a container image.
const ServerImage = "registry.coffeylabs.org/inbuxa/inbuxa-server:" + ServerVersion

// ServerSHA256 is the SHA-256 of each architecture's release tarball, copied
// from the release's SHA256SUMS when the version was moved and checked here
// rather than fetched beside the download: a checksum that arrives with the
// file only proves the file arrived.
var ServerSHA256 = map[string]string{
	"amd64": "bd002f4c5278596ef814a863e4f87a54837d5e014dec8891a5f66b5590e47d6b",
	"arm64": "c585ea5afe028e84ab4c69a6a0923e138446796389d907f8d3c7cedaba1fc825",
}

// ServerTarball is where the release's tarball for an architecture is.
func ServerTarball(arch string) string {
	return fmt.Sprintf("https://git.coffeylabs.org/inbuxa/inbuxa-server/releases/download/v%s/inbuxa-linux-%s.tar.gz",
		ServerVersion, arch)
}

// The console and the webmail as files, for host installs. Their releases
// attach these since inbuxa-admin #106 and inbuxa-webmail #68; a version
// with an empty checksum has none to install, and the host shape is
// unavailable until one does. INBUXA_CONSOLE_TARBALL and
// INBUXA_WEBMAIL_TARBALL name a local file instead, for testing.
const (
	ConsoleVersion = "27.1-rc.17"
	ConsoleSHA256  = "7668fe7110c94b259cc89964e9e7b5277caf48f457d8414100c3c101201d8be8"
	WebmailVersion = "27.1-rc.5" // its tag is inbuxa-v<this>
)

// WebmailSHA256 is the SHA-256 of each architecture's webmail tarball.
var WebmailSHA256 = map[string]string{
	"amd64": "b11fad3de32eeb865d2cad2da4678d662a5d43e043d3aa4592b440b45be0e9ed",
	"arm64": "48be31c7b150e3b6363671385dfed9219a4c9966a6a3ad7a5236cc11c9d85a52",
}

// ConsoleTarball is where the console's release tarball is.
func ConsoleTarball() string {
	return fmt.Sprintf("https://git.coffeylabs.org/inbuxa/inbuxa-admin/releases/download/v%s/inbuxa-admin.tar.gz", ConsoleVersion)
}

// WebmailTarball is where the webmail's release tarball for an
// architecture is. Its tags are inbuxa-v<version>.
func WebmailTarball(arch string) string {
	return fmt.Sprintf("https://git.coffeylabs.org/inbuxa/inbuxa-webmail/releases/download/inbuxa-v%s/inbuxa-webmail-linux-%s.tar.gz",
		WebmailVersion, arch)
}

// inbuxa Relay, for a machine of its own in front of the server: the
// static inbuxa-relay binary each server release attaches since
// inbuxa-server #271, with a .sig file beside it signed by an inbuxa release
// key. Its release is the server's, so a relay this installer puts in place
// runs the version the server it joins does. A version with an empty
// checksum has no relay to install. INBUXA_RELAY_BINARY names a local file
// instead, for testing.
//
// RelaySHA256 is the SHA-256 of each architecture's relay binary.
var RelaySHA256 = map[string]string{
	"amd64": "0e8af56c045de295ee2ce318c23ce96e2ef5b25aea77862a85a9df724285ca12",
	"arm64": "38012ff36e7e50377c5130d8e708e2e11221d7306939f7db93560d069483c515",
}

// RelayKeys are the inbuxa release keys that sign a relay binary, as
// crates/relay-proto/src/release.rs holds them: the CI key, then the one
// kept offline. Base64url, no padding.
var RelayKeys = []string{
	"J0kY_vzXTC8T8gxErBMYI6cVk4Bwki65HlJGoDp15uw",
	"vGsTE95CJ3vbGKVtfQctFEp0fOZ4wQ9QWTOFe0KF7_8",
}

// RelayVersion is the server's release as the relay names itself, in its
// --version and its .sig file: the Cargo version, 27.1.0-rc.4 for 27.1-rc.4.
func RelayVersion() string { return CargoVersion(ServerVersion) }

// CargoVersion is a release version with the patch number SemVer wants:
// 27.1 is 27.1.0, and 27.1-rc.4 is 27.1.0-rc.4.
func CargoVersion(v string) string {
	core, pre, hasPre := strings.Cut(v, "-")
	if strings.Count(core, ".") == 1 {
		core += ".0"
	}
	if hasPre {
		return core + "-" + pre
	}
	return core
}

// RelayTarget is what a relay binary's .sig file names an architecture.
func RelayTarget(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64-linux-musl"
	case "arm64":
		return "aarch64-linux-musl"
	}
	return arch + "-linux-musl"
}

// RelayBinary is where the release's relay binary for an architecture is;
// its .sig file is beside it, with ".sig" added.
func RelayBinary(arch string) string {
	return fmt.Sprintf("https://git.coffeylabs.org/inbuxa/inbuxa-server/releases/download/v%s/inbuxa-relay-%s",
		ServerVersion, RelayTarget(arch))
}
