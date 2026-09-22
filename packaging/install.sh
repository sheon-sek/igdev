#!/usr/bin/env bash
# Install igdev from GitHub Releases with checksum verification.
#
#   curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash
#
# The installer never runs a binary it has not verified, and it never updates one
# that is already installed (ADR 0002: notice-only, no self-update). When the
# install directory is not already on PATH, the installer registers it in the
# user's login profile so the very next shell finds `igdev` by name.
#
# Options (flags win over the environment):
#   --version <ver>   release to install; default is the newest one advertised
#   --prefix <dir>    install directory; default $HOME/.local/bin
#   --repo-url <url>  release host to use; default https://github.com/sheon-sek/igdev
#   --base-url <url>  exact download directory, overriding --repo-url and the
#                     release layout; anything curl or wget accepts, file:// too
#   IGDEV_INSTALL_VERSION, IGDEV_INSTALL_PREFIX, IGDEV_INSTALL_REPO_URL,
#   IGDEV_INSTALL_BASE_URL
set -euo pipefail

REPO_URL="${IGDEV_INSTALL_REPO_URL:-https://github.com/sheon-sek/igdev}"
BASE_URL="${IGDEV_INSTALL_BASE_URL:-}"
VERSION="${IGDEV_INSTALL_VERSION:-}"
PREFIX="${IGDEV_INSTALL_PREFIX:-${HOME:?HOME must be set}/.local/bin}"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2-}"; shift 2 ;;
    --prefix) PREFIX="${2-}"; shift 2 ;;
    --repo-url) REPO_URL="${2-}"; shift 2 ;;
    --base-url) BASE_URL="${2-}"; shift 2 ;;
    -h | --help)
      printf '%s\n' \
        'usage: install.sh [--version <ver>] [--prefix <dir>] [--repo-url <url>] [--base-url <url>]' \
        '  installs igdev from a checksum-verified release tarball' \
        '  default prefix is $HOME/.local/bin, registered in the login profile if off PATH'
      exit 0
      ;;
    *)
      printf 'igdev-install: unknown argument %s (try --help)\n' "$1" >&2
      exit 2
      ;;
  esac
done

# The version is spliced into a URL and a filename, so it must be a plain token.
case "$VERSION" in
  '') ;;
  *[!A-Za-z0-9._+-]*)
    printf 'igdev-install: --version %s is not a valid release version (letters, digits, . _ + - only)\n' \
      "'$VERSION'" >&2
    exit 2
    ;;
esac

if [ -z "$BASE_URL" ]; then
  # A pinned version must come from its own tag: the "latest" download directory
  # stops being it the moment a newer release is published.
  if [ -n "$VERSION" ]; then
    BASE_URL="$REPO_URL/releases/download/v$VERSION"
  else
    BASE_URL="$REPO_URL/releases/latest/download"
  fi
fi
BASE_URL="${BASE_URL%/}"

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

# Everything this script writes lives in one temp directory, plus one staging name
# inside the prefix; the trap removes both, including on the failure paths. The
# login profile is only ever appended to, between its own marker pair.
tmp="$(mktemp -d "${TMPDIR:-/tmp}/igdev-install.XXXXXX")"
target="$PREFIX/igdev"
staged="$PREFIX/.igdev.staged.$$"
cleanup() {
  rm -rf "${tmp:-}"
  rm -f "${staged:-}"
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

# Match the artifact name literally, never as a pattern: the version is
# user-supplied and `.*` must not select something arbitrary.
platform="$os-$arch"
if [ -n "$VERSION" ]; then
  artifact="igdev-$VERSION-$platform.tar.gz"
  hash="$(awk -v want="$artifact" '$2 == want { print $1; exit }' "$tmp/checksums.txt")"
else
  # The artifact name carries the version, so an unpinned install reads it back
  # out of the newest release's own checksum list.
  line="$(awk -v platform="$platform" \
    '$2 ~ ("^igdev-[^ ]*-" platform "[.]tar[.]gz$") { print; exit }' "$tmp/checksums.txt")"
  artifact="${line#*  }"
  hash="${line%% *}"
fi
if [ -z "$artifact" ] || [ -z "$hash" ]; then
  printf 'igdev-install: no artifact for %s in this release\n' "$platform" >&2
  exit 1
fi

fetch "$BASE_URL/$artifact" "$tmp/$artifact"

printf 'igdev-install: verifying %s\n' "$artifact"
if ! printf '%s  %s\n' "$hash" "$artifact" | (cd "$tmp" && $checksum_tool --status -c -); then
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
# inside one filesystem is atomic, so there is no moment when the installed
# binary is half-written.
cp "$binary" "$staged"
chmod 0755 "$staged"
mv -f "$staged" "$target"
staged=""

if ! "$target" version >/dev/null 2>&1; then
  printf 'igdev-install: installed binary at %s did not run\n' "$target" >&2
  exit 1
fi

# Being installed is not being usable: if the prefix is off PATH, a brand-new
# login shell must still find `igdev` by name.
on_path() {
  case ":${PATH:-}:" in *":$PREFIX:"*) return 0 ;; *) return 1 ;; esac
}
marker='# igdev: install prefix on PATH (added by igdev install.sh)'
if ! on_path; then
  profile=""
  for candidate in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
    if [ -f "$candidate" ]; then profile="$candidate"; break; fi
  done
  if [ -z "$profile" ]; then profile="$HOME/.profile"; fi
  if [ -f "$profile" ] && grep -F -q "$marker" "$profile"; then
    printf 'igdev-install: %s already registers %s\n' "${profile##*/}" "$PREFIX"
  else
    {
      printf '\n%s\n' "$marker"
      printf 'case ":$PATH:" in *":%s:"*) ;; *) PATH="%s${PATH:+:$PATH}" ;; esac\n' "$PREFIX" "$PREFIX"
      printf 'export PATH\n'
    } >>"$profile"
    printf 'igdev-install: added %s to PATH in %s (open a new shell, or source it)\n' \
      "$PREFIX" "$profile"
  fi
fi

printf 'igdev-install: installed %s to %s\n' "$("$target" version | head -n 1)" "$target"
