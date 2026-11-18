// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package release is which version of the mail server this installer puts on
// a machine, as an image or as a binary, and what that binary must hash to.
//
// One version for both shapes, so a server moved from a container to the host
// is the same program, and the plan, the image tag and the download cannot
// name three different releases.
package release

import "fmt"

// ServerVersion is the inbuxa-server release this installer installs.
const ServerVersion = "2026.11.18"

// ServerImage is that release as a container image.
const ServerImage = "registry.coffeylabs.org/inbuxa/inbuxa-server:" + ServerVersion

// Caddy, the proxy in front of the console and the webmail, which holds
// their TLS keys, from Docker Hub. It is pinned by the digest of its image
// index, as the binaries are by their checksums: a tag there moves, and even
// a full version's tag is pushed again when its image is rebuilt (for a new
// Alpine, say). CaddyVersion is the tag the digest was read from, X.Y.Z of
// caddy:X.Y.Z-alpine. This line has no pin-check: the pin is moved by hand,
// like the versions here.
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
	"amd64": "e775e37a1a859c044ebc71761dd452a5914b49a32a44eeff06aac8dee3ca3518",
	"arm64": "705de8382bf211529f34ffee14fd04544d5c88b00f84783240a465abe748931f",
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
	ConsoleVersion = "2026.10.26.7"
	ConsoleSHA256  = "3fea4aed273258d45d5f956dccca94039e66a5131793de47d29a60fd5fc90834"
	WebmailVersion = "2026.10.26-gcff6f1f" // its tag is inbuxa-v<this>
)

// WebmailSHA256 is the SHA-256 of each architecture's webmail tarball.
var WebmailSHA256 = map[string]string{
	"amd64": "851ef48ae71e349c6ff2772618a1afad0e664ba5ee94041a661b13da1c282a38",
	"arm64": "852b9f85423b9b73600fddf7d2cdefc6b58473fa157db8d9df31384e746ee1dd",
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
