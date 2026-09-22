//go:build !(linux || darwin)

package jython

import "os"

// acquire is a no-op where POSIX flock is unavailable; igdev ships for Linux and
// macOS (ADR 0002), so this only keeps other platforms buildable.
func acquire(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return func() { _ = file.Close() }, nil
}
