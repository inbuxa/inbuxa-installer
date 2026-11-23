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
const ServerVersion = "27.1-rc.41"

// ServerImage is that release as a container image.
const ServerImage = "registry.coffeylabs.org/inbuxa/inbuxa-server:" + ServerVersion

// Caddy, the proxy in front of the console and the webmail, which holds
// their TLS keys, from Docker Hub. It is pinned by the digest of its image
// index, as the binaries are by their checksums: a tag there moves, and even
// a full version's tag is pushed again when its image is rebuilt (for a new
// Alpine, say). CaddyVersion is the tag the digest was read from, X.Y.Z of
// caddy:X.Y.Z-alpine, which pin-check watches for a newer one or a rebuild.
// The digest is read with
// `docker buildx imagetools inspect docker.io/library/caddy:<version>-alpine`.
const (
	CaddyVersion = "2.11.9"
	CaddyDigest  = "sha256:003a97808e781aa9f7e75cc2f43d34d461099031faf5e20faefb9f61126836f0"
)

// CaddyImage is that image, by its digest alone, which every runtime the
// installer drives reads the same way.
const CaddyImage = "docker.io/library/caddy@" + CaddyDigest

// ServerSHA256 is the SHA-256 of each architecture's release tarball, copied
// from the release's SHA256SUMS when the version was moved and checked here
// rather than fetched beside the download: a checksum that arrives with the
// file only proves the file arrived.
var ServerSHA256 = map[string]string{
	"amd64": "81f88cd67c60542b68562ca7675cc4c768ae4056165d31bd74e2d73685c5cc30",
	"arm64": "be3f50ab5c37b36ef051d72c9fc652b3337526f20468a9dca2c7a982a9de9953",
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
	ConsoleVersion = "27.1-rc.24"
	ConsoleSHA256  = "70a4f244b5d73711bb604e75e1e8cb00d3824578740547c3ecdbea25a1122932"
	WebmailVersion = "27.1-rc.14" // its tag is inbuxa-v<this>
)

// WebmailSHA256 is the SHA-256 of each architecture's webmail tarball.
var WebmailSHA256 = map[string]string{
	"amd64": "1aaffc0b4570e5ad03d232297d69499d79333b50fe90d9259dc433060885b2eb",
	"arm64": "20b59ad60193cbec8ea34e6287ae7d3232ffcca304f4d61de44f1198b7af4ff7",
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
	"amd64": "39be7348eda2163d0746cbe42fb4850b32610979cf16c46b05950d404aec0bc9",
	"arm64": "5ef8de5b195c4197f696e9c8533fd58f75995667e44b2f64c68bdddb257773e9",
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
