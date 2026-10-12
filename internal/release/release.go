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
const ServerVersion = "2026.10.12.1"

// ServerImage is that release as a container image.
const ServerImage = "registry.coffeylabs.org/inbuxa/inbuxa-server:" + ServerVersion

// ServerSHA256 is the SHA-256 of each architecture's release tarball, copied
// from the release's SHA256SUMS when the version was moved and checked here
// rather than fetched beside the download: a checksum that arrives with the
// file only proves the file arrived.
var ServerSHA256 = map[string]string{
	"amd64": "14b343c04fa05d2ff637774d332ca7738f16179817b0ed9936ee4d551ef34fa4",
	"arm64": "0f5ad50b0ec0587057ec058f4856d6674ad2971d210f264eaa98f60b90e8e64e",
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
	ConsoleVersion = "2026.10.12.2"
	ConsoleSHA256  = "73583f5220bc5199a799e1f71d00e5f26e4a1171fc10ec6171c1b0348e878b85"
	WebmailVersion = "2026.10.12-g6e1a1cb" // its tag is inbuxa-v<this>
)

// WebmailSHA256 is the SHA-256 of each architecture's webmail tarball.
var WebmailSHA256 = map[string]string{
	"amd64": "399d2575a2d9426b0b28d9b64f35ca44602b4bd2817e23a7a29c7dc22a978a3f",
	"arm64": "33a2cc435168739efb34e202124ff5526b7445f8de8ba8f62dc23dd3d0a94e7f",
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
