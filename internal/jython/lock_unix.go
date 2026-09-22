//go:build linux || darwin

package jython

import (
	"os"
	"syscall"
)

// acquire takes an exclusive advisory lock on path and returns the release. The
// lock is on the file itself, so it serialises exactly the parallel processes
// fetching one cache entry and nothing else.
func acquire(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
