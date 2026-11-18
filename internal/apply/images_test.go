// SPDX-FileCopyrightText: 2026 Coffey Labs LLC
// SPDX-License-Identifier: AGPL-3.0-or-later

package apply

import (
	"regexp"
	"testing"
)

// Everything this installer downloads as a file is checked against a
// checksum in its source. Caddy, the proxy that holds the TLS keys, was
// pulled by a floating tag from Docker Hub instead, so whoever could push
// that tag chose what ran (INSTALLER-5). It is pinned by its digest now.
func TestCaddyIsPinnedByDigest(t *testing.T) {
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(DefaultCaddyImage) {
		t.Errorf("DefaultCaddyImage = %q, not pinned by digest", DefaultCaddyImage)
	}
}

// The suite's own images name one exact release, never a moving tag. The
// webmail's releases on this line are named by version and commit.
func TestSuiteImagesNameAnExactRelease(t *testing.T) {
	exact := regexp.MustCompile(`:\d+\.\d+(\.\d+)*(-rc\.\d+|-g[0-9a-f]+)?$`)
	for _, img := range []string{DefaultServerImage, DefaultConsoleImage, DefaultWebmailImage} {
		if !exact.MatchString(img) {
			t.Errorf("%s does not name an exact release", img)
		}
	}
}
