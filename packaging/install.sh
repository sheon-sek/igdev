#!/usr/bin/env bash
# Install igdev from GitHub Releases with checksum verification.
#
#   curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash
#
# The installer never runs a binary it has not verified, and it never updates one
# that is already installed (ADR 0002: notice-only, no self-update).
#
# Options (flags win over the environment):
#   --version <ver>   release to install; default is the newest entry in the
#                     release's own checksums.txt
#   --prefix <dir>    install directory; default $HOME/.local/bin
#   --base-url <url>  where to fetch from; anything curl or wget accepts,
#                     including file:// — default GitHub Releases "latest"
#   IGDEV_INSTALL_VERSION, IGDEV_INSTALL_PREFIX, IGDEV_INSTALL_BASE_URL
set -euo pipefail

BASE_URL="${IGDEV_INSTALL_BASE_URL:-https://github.com/sheon-sek/igdev/releases/latest/download}"
VERSION="${IGDEV_INSTALL_VERSION:-}"
PREFIX="${IGDEV_INSTALL_PREFIX:-${HOME:?HOME must be set}/.local/bin}"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
    --prefix) PREFIX="${2:?--prefix needs a value}"; shift 2 ;;
    --base-url) BASE_URL="${2:?--base-url needs a value}"; shift 2 ;;
    -h | --help)
      printf '%s\n' \
        'usage: install.sh [--version <ver>] [--prefix <dir>] [--base-url <url>]' \
        '  installs igdev from a checksum-verified release tarball' \
        '  default prefix is $HOME/.local/bin'
      exit 0
      ;;
    *)
      printf 'igdev-install: unknown argument %s (try --help)\n' "$1" >&2
      exit 2
      ;;
  esac
done

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *)
    printf 'igdev-install: unsupported operating system %s (Linux and macOS only)\n' "$(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)
    printf 'igdev-install: unsupported cpu architecture %s (amd64 and arm64 only)\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" -- "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" -- "$1"; }
else
  printf 'igdev-install: need curl or wget to download a release\n' >&2
  exit 1
fi

# Everything this script writes lives in one temp directory, and one staging
# name inside the prefix, both removed by the trap even on failure.
tmp="$(mktemp -d "${TMPDIR:-/tmp}/igdev-install.XXXXXX")"
target="$PREFIX/igdev"
staged="$PREFIX/.igdev.staged.$$"
cleanup() {
  rm -rf "$tmp"
  rm -f "$staged"
}
trap cleanup EXIT INT TERM

if command -v sha256sum >/dev/null 2>&1; then
  checksum_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  checksum_tool="shasum -a 256"
else
  printf 'igdev-install: need sha256sum or shasum to verify a release\n' >&2
  exit 1
fi

printf 'igdev-install: reading checksums from %s\n' "$BASE_URL"
fetch "$BASE_URL/checksums.txt" "$tmp/checksums.txt"

# The artifact name carries the version, so an unpinned install reads the
# version back out of the newest release's own checksum list.
wanted="igdev-[^ ]*-${os}-${arch}\.tar\.gz"
if [ -n "$VERSION" ]; then
  wanted="igdev-${VERSION}-${os}-${arch}\.tar\.gz"
fi

line="$(grep -m1 -E "^[0-9a-f]+  ${wanted}\$" "$tmp/checksums.txt" || true)"
if [ -z "$line" ]; then
  printf 'igdev-install: no artifact for %s/%s in this release\n' "$os" "$arch" >&2
  exit 1
fi
artifact="${line#*  }"

fetch "$BASE_URL/$artifact" "$tmp/$artifact"

printf 'igdev-install: verifying %s\n' "$artifact"
if ! (cd "$tmp" && grep -E "  ${artifact}\$" checksums.txt | $checksum_tool --status -c -); then
  printf 'igdev-install: CHECKSUM MISMATCH for %s - refusing to install\n' "$artifact" >&2
  exit 1
fi

mkdir -p "$tmp/unpack"
tar -xzf "$tmp/$artifact" -C "$tmp/unpack"
binary="$(find "$tmp/unpack" -name igdev -type f | head -n 1)"
if [ -z "$binary" ]; then
  printf 'igdev-install: %s contained no igdev binary\n' "$artifact" >&2
  exit 1
fi

mkdir -p "$PREFIX"
# Copy into a staging name in the destination directory, then rename: a rename
# within one filesystem is atomic, so there is no moment where the installed
# binary is half-written.
cp "$binary" "$staged"
chmod 0755 "$staged"
mv -f "$staged" "$target"

if ! "$target" version >/dev/null 2>&1; then
  printf 'igdev-install: installed binary at %s did not run\n' "$target" >&2
  exit 1
fi

printf 'igdev-install: installed %s to %s\n' "$("$target" version | head -n 1)" "$target"
case ":${PATH:-}:" in
  *":$PREFIX:"*) ;;
  *) printf 'igdev-install: add %s to PATH to use igdev\n' "$PREFIX" >&2 ;;
esac
