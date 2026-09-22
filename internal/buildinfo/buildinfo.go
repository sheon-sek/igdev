// Package buildinfo carries the version identity of the binary. The release
// workflow stamps Version and Commit with -ldflags; dev builds keep the
// defaults so the CLI Contract Version stays the only version-like constant
// tests freeze.
package buildinfo

// Version is the release semver of this binary, without a leading "v".
var Version = "0.1.0"

// Commit is the git object this binary was built from, or "unset".
var Commit = "unset"

// InstallCommand is the one-line installer published with every release. The
// update notice points at it.
const InstallCommand = "curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash"
