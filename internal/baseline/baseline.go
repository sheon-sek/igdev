// Package baseline manages the staged .gwbk Backup a disposable Gateway is
// seeded from: the checkout-local copy, its provenance record, and the Ignition
// restore arguments the next fresh launch applies.
//
// The staged file is the state, and igdev keeps it out of everything it
// regenerates: it lives in `.igdev/baseline/`, not in `.igdev/runtime/`, so
// re-running `setup` (including the run that repairs a stale Setup Stamp) never
// discards it. The rendered Compose file mounts that directory into the Gateway
// read-only, and the restore argument names a path under the mount.
package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
)

// Frozen names and modes of a checkout's Baseline.
const (
	// DirName is the Checkout Setup subdirectory the Baseline lives in. It is
	// deliberately outside `.igdev/runtime/`, which setup owns and re-renders.
	DirName = "baseline"
	// FileName is the staged Baseline's fixed name: the restore argument names
	// this file inside the Gateway, so the name is part of the wiring.
	FileName = "restore.gwbk"
	// RecordFileName is the provenance record written beside the staged file:
	// where the backup came from, and what it was when it was staged.
	RecordFileName = "baseline.json"
	// MountPoint is where DirName is mounted inside the Gateway container. The
	// rendered Compose file mounts it read-only; ContainerPath lives under it.
	MountPoint = "/restore"
	// Extension is the only backup format Ignition restores.
	Extension = ".gwbk"
	// FileMode is the staged file's mode: a backup is not a secret, and the
	// container reads it as an ordinary file.
	FileMode = 0o644
	// DirMode is the Baseline directory's mode.
	DirMode = 0o755
)

// Record is the provenance of the staged Baseline: the file it was copied from,
// and the size and digest it had at that moment. It is written after the copy,
// so a record that disagrees with the staged file is never the file's truth.
type Record struct {
	Source   string `json:"source"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	StagedAt string `json:"staged_at"`
}

// State is one checkout's Baseline: whether a staged file is there, and what is
// known about it. Provenance (Source, SHA256, StagedAt) comes from the record
// and is empty when the record is absent or describes a different file.
type State struct {
	Staged   bool
	Path     string
	Bytes    int64
	Source   string
	SHA256   string
	StagedAt string
}

// Dir is the Baseline directory inside a Checkout Setup's state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, DirName) }

// FilePath is the staged Baseline's host path inside dir.
func FilePath(dir string) string { return filepath.Join(dir, FileName) }

// RecordPath is the provenance record's host path inside dir.
func RecordPath(dir string) string { return filepath.Join(dir, RecordFileName) }

// ContainerPath is where the staged Baseline appears inside the Gateway.
func ContainerPath() string { return path.Join(MountPoint, FileName) }

// Args is the Ignition launcher arguments that restore the staged Baseline. They
// reach the Gateway through the rendered Compose file's command, which
// interpolates the variable Compose carries them in.
func Args() string { return "-r " + ContainerPath() }

// Validate reports whether source is something that can be staged. A file that
// is not a .gwbk is a wrong invocation (exit 2); a path that is not an existing,
// readable, regular file is a named failure at exit 1.
func Validate(source string) *contract.Fault {
	if !strings.EqualFold(filepath.Ext(source), Extension) {
		return contract.UsageFault(
			fmt.Sprintf("a Baseline must be a %s Gateway backup, got %q", Extension, source),
			contract.Remediation{Command: "igdev baseline set <file.gwbk>",
				Why: "stage an Ignition Gateway backup"})
	}
	info, err := os.Stat(source)
	switch {
	case os.IsNotExist(err):
		return contract.NewFault(contract.CodeBaselineMissing, contract.ExitFailure,
			fmt.Sprintf("the Baseline file %s does not exist", source)).
			WithRemediation(contract.Remediation{Command: "igdev baseline set <file.gwbk>",
				Why: "stage a Baseline backup that exists on this machine"})
	case err != nil:
		return invalidFault(source, err)
	case !info.Mode().IsRegular():
		return invalidFault(source, fmt.Errorf("it is %s, not a file", fileKind(info)))
	}
	return nil
}

// invalidFault is a source that exists but cannot be staged as a file.
func invalidFault(source string, cause error) *contract.Fault {
	return contract.NewFault(contract.CodeBaselineInvalid, contract.ExitFailure,
		fmt.Sprintf("%s cannot be staged as a Baseline backup: %v", source, cause)).
		WithCause(cause).
		WithRemediation(contract.Remediation{Command: "igdev baseline set <file.gwbk>",
			Why: "stage a Baseline backup igdev can read as a file"})
}

// fileKind names what a non-regular path is, for the message.
func fileKind(info os.FileInfo) string {
	switch {
	case info.IsDir():
		return "a directory"
	case info.Mode()&os.ModeSymlink != 0:
		return "a symlink"
	default:
		return "a special file"
	}
}

// Stage copies source into dir as the staged Baseline, replacing whatever was
// staged before: a Baseline is checkout-local user state, so there is no tracked
// file to diff and no confirmation to ask for. The copy is streamed through one
// hash and renamed into place, so a large backup is never held in memory and a
// failed copy never replaces a staged file.
func Stage(source, dir string, now time.Time) (State, *contract.Fault) {
	if fault := Validate(source); fault != nil {
		return State{}, fault
	}
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return State{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot create %s: %v", dir, err)).WithCause(err)
	}
	digest, size, err := copyFile(source, FilePath(dir))
	if err != nil {
		return State{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot stage %s: %v", source, err)).WithCause(err)
	}
	record := Record{
		Source:   source,
		Bytes:    size,
		SHA256:   digest,
		StagedAt: now.UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err == nil {
		err = atomicfile.Write(RecordPath(dir), append(raw, '\n'), FileMode, DirMode)
	}
	if err != nil {
		// The staged file is what the Gateway restores from, so it stays: a run
		// that could not record where it came from is still a staged Baseline.
		return State{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot record the staged Baseline in %s: %v", RecordPath(dir), err)).WithCause(err)
	}
	return State{
		Staged:   true,
		Path:     FilePath(dir),
		Bytes:    size,
		Source:   source,
		SHA256:   digest,
		StagedAt: record.StagedAt,
	}, nil
}

// Read reports the staged Baseline in dir. A record that does not describe the
// staged file (a different size) is not reported as provenance: the file is the
// state, and igdev never claims a source that is not true.
func Read(dir string) State {
	path := FilePath(dir)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return State{}
	}
	state := State{Staged: true, Path: path, Bytes: info.Size()}
	if record, ok := readRecord(dir); ok && record.Bytes == info.Size() {
		state.Source, state.SHA256, state.StagedAt = record.Source, record.SHA256, record.StagedAt
	}
	return state
}

// Clear removes the staged Baseline and its record, and reports the paths it
// removed. It leaves the directory in place: that directory is the mount point
// the rendered Compose file names, and clearing means "restore nothing", not
// "unmount".
func Clear(dir string) ([]string, *contract.Fault) {
	removed := []string{}
	for _, path := range []string{FilePath(dir), RecordPath(dir)} {
		switch err := os.Remove(path); {
		case err == nil:
			removed = append(removed, path)
		case os.IsNotExist(err):
		default:
			return removed, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				fmt.Sprintf("cannot remove %s: %v", path, err)).WithCause(err)
		}
	}
	return removed, nil
}

// readRecord reads the provenance record. An absent, unreadable, or malformed
// record reports false: a record is a note about a staged file, never a reason
// for a command to fail.
func readRecord(dir string) (Record, bool) {
	raw, err := os.ReadFile(RecordPath(dir))
	if err != nil {
		return Record{}, false
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, false
	}
	return record, true
}

// copyFile streams source to dest through one hash and renames it into place:
// the destination is either its old bytes or the complete new file, never a
// partial copy, and the whole backup never sits in memory.
func copyFile(source, dest string) (digest string, size int64, err error) {
	src, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".tmp-")
	if err != nil {
		return "", 0, err
	}
	name := tmp.Name()
	discard := func() {
		tmp.Close()
		os.Remove(name)
	}
	hash := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, hash), src)
	if err != nil {
		discard()
		return "", 0, err
	}
	if err := tmp.Chmod(FileMode); err != nil {
		discard()
		return "", 0, err
	}
	// Only the sync makes the bytes durable before the rename: a state file must
	// not come back truncated after a crash.
	if err := tmp.Sync(); err != nil {
		discard()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", 0, err
	}
	if err := os.Rename(name, dest); err != nil {
		os.Remove(name)
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
