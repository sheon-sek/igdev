//go:build !(linux || darwin)

package updater

import "os"

const lockUnblock = 0

// flockExclusive is a no-op where POSIX flock is unavailable; igdev ships for
// Linux and macOS (ADR 0002), so this only keeps other platforms buildable.
func flockExclusive(file *os.File) error { return nil }
