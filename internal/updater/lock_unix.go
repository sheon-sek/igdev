//go:build linux || darwin

package updater

import (
	"os"
	"syscall"
)

const lockUnblock = syscall.LOCK_UN

// flockExclusive takes an exclusive advisory lock, blocking only until the peer
// holding it finishes its (bounded) release query.
func flockExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
