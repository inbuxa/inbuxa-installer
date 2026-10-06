#!/bin/bash
# SPDX-FileCopyrightText: 2026 Coffey Labs LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Build the release binaries.
#
# Usage: scripts/build-release.sh TAG OUTDIR
#        scripts/build-release.sh v2026.10.5 dist
#
# CI runs exactly this on a v* tag (.gitea/workflows/release.yml), so a
# release can be built and checked on any machine with Go before tagging.
#
# Static (CGO off), so one binary runs on every distribution the installer
# supports, glibc or not, and both architectures cross-compile on one
# machine. The file names carry no version (the release's tag is in its URL);
# the tag is stamped in, so `inbuxa version` and the release cannot disagree.
set -euo pipefail

TAG="${1:?usage: build-release.sh TAG OUTDIR}"
OUT="${2:?usage: build-release.sh TAG OUTDIR}"
[[ "$TAG" =~ ^v[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}(\.[0-9]+)?$ ]] \
  || { echo "tag $TAG is not v<year>.<month>.<day>[.<n>]" >&2; exit 1; }

mkdir -p "$OUT"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${TAG#v}" -o "$OUT/inbuxa-linux-$arch" ./cmd/inbuxa
done
(cd "$OUT" && sha256sum inbuxa-linux-amd64 inbuxa-linux-arm64 > SHA256SUMS)
cat "$OUT/SHA256SUMS"
