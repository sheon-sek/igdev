// Package atomicfile replaces a file in one rename: a temp file in the target
// directory, fsynced, then renamed over the destination. Nothing observes a
// partial file, a crash leaves the old bytes in place, and no .bak is ever
// created.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write stores data at path with mode, creating the parent directory with
// dirMode when it is missing. The mode is applied with chmod, so it does not
// depend on the process umask: a file igdev declares 0600 is 0600.
func Write(path string, data []byte, mode, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	discard := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		discard()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		discard()
		return err
	}
	// The rename is atomic, but only the sync makes the bytes durable before it:
	// a state file must not come back empty after a crash.
	if err := tmp.Sync(); err != nil {
		discard()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
