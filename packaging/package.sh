#!/usr/bin/env bash
# Build the release artifacts for igdev: one versioned tarball per target plus a
# sha256 checksum file. The release workflow runs this on a tag push; a developer
# can run it to produce the same tree locally.
#
# Usage: packaging/package.sh [version]
#   version defaults to `git describe --tags --always`; pass an explicit one
#   (for example 0.1.0) to build a deterministic tree for testing.
#
# Output: dist/<version>/igdev-<version>-<os>-<arch>.tar.gz holding the binary
# (and LICENSE when the repository has one) for linux/{amd64,arm64} and
# darwin/{amd64,arm64}, plus dist/<version>/checksums.txt.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
VERSION="${VERSION#v}"
# IGDEV_DIST_DIR lets a test (or a developer) build somewhere other than dist/.
DIST="${IGDEV_DIST_DIR:-dist}/$VERSION"
MODULE="github.com/sheon-sek/igdev"
COMMIT="$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Every target the installer recognizes, so a pinned install can never discover
# mid-download that its platform was never published (ADR 0002: Linux and macOS,
# amd64 and arm64; WSL uses the Linux binary, Windows native is unsupported).
TARGETS=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
)

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@" | awk '{print $1}'
  else
    shasum -a 256 "$@" | awk '{print $1}'
  fi
}

rm -rf "$DIST"
mkdir -p "$DIST"

for target in "${TARGETS[@]}"; do
  read -r goos goarch <<<"$target"
  name="igdev-$VERSION-$goos-$goarch"
  stage="$DIST/$name"
  mkdir -p "$stage"

  echo "building $name (commit $COMMIT)"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath \
      -ldflags "-s -w -X $MODULE/internal/buildinfo.Version=$VERSION -X $MODULE/internal/buildinfo.Commit=$COMMIT" \
      -o "$stage/igdev" ./cmd/igdev

  # LICENSE is optional because the repository has not chosen one yet; ticket
  # 13 settles that, and the packaging must not break before it does.
  if [ -f LICENSE ]; then cp LICENSE "$stage/LICENSE"; fi

  tar -C "$DIST" -czf "$DIST/$name.tar.gz" "$name"
  rm -rf "$stage"
done

# checksums.txt is `<sha256>  <filename>`, the format `shasum -a 256 -c` accepts.
: >"$DIST/checksums.txt"
for artifact in "$DIST"/*.tar.gz; do
  printf '%s  %s\n' "$(sha256 "$artifact")" "$(basename "$artifact")" >>"$DIST/checksums.txt"
done

printf 'igdev-package: artifacts in %s:\n' "$DIST"
ls -1 "$DIST"
