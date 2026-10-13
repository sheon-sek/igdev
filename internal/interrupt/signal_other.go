//go:build !(linux || darwin)

package interrupt

import "os"

// notify handles nothing where POSIX signals are unavailable; igdev ships for
// Linux and macOS (ADR 0002), so this only keeps other platforms buildable.
func notify() <-chan os.Signal { return nil }

func reraise(os.Signal) { os.Exit(1) }
