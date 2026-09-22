package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
)

// The permissions staging writes with: the artifact is ordinary checkout state,
// the directory matches the rest of the Checkout Setup.
const (
	stagedMode = 0o644
	stagedDir  = 0o755
)

// Staged is one artifact `Stage` placed in a checkout's module directory.
type Staged struct {
	Record
	// Path is the staged artifact's absolute path.
	Path string
	// Bytes is the staged size, which is what was copied, not what the archive
	// claims to expand to.
	Bytes int64
	// Replaced reports that an artifact of the same file name was already
	// staged, so this copy took its place.
	Replaced bool
}

// Validate reads one `.modl` and reports the metadata it declares, or the fault
// that says why it cannot be staged. Everything it checks is a property of the
// file: that it is a readable zip, that it carries a module.xml, that the
// module.xml passes the zip-bomb guard, and that it yields an id the Project
// Contract would accept as a `[modules].enabled` entry.
//
// The archive is metadata-only: igdev reads id, name, and version and never
// treats anything else inside it as knowledge (ADR 0005).
func Validate(path string) (Record, *contract.Fault) {
	if !strings.EqualFold(filepath.Ext(path), ".modl") {
		return Record{}, archiveFault(path, "its file name does not end in .modl")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Record{}, archiveFault(path, err.Error())
	}
	if info.IsDir() {
		return Record{}, archiveFault(path, "it is a directory")
	}
	record := read(path, filepath.Base(path))
	if record.ID == "" {
		return Record{}, archiveFault(path, record.Err)
	}
	if !project.ValidModuleID(record.ID) {
		return Record{}, archiveFault(path, fmt.Sprintf("its module.xml declares the id %q, which is not a module id", record.ID))
	}
	return record, nil
}

// archiveFault is the fault for an artifact that cannot be staged. It is one code
// for every reason: the caller asked for a module archive and the file is not
// one, and the message says which check failed.
func archiveFault(path, why string) *contract.Fault {
	if why == "" {
		why = "it cannot be read"
	}
	return contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
		fmt.Sprintf("%s is not a module archive igdev can stage: %s", path, why)).
		WithRemediation(contract.Remediation{
			Command: "igdev help module add",
			Why:     "show what `module add` accepts and what a .modl has to contain",
		})
}

// Stage copies src into dir under its own file name and reports the staged
// artifact. The copy is atomic — a temp file in the destination directory, then
// a rename — so a reader sees either the previous artifact or the new one, never
// a partial file, and no backup file is ever left behind.
func Stage(dir, src string) (Staged, *contract.Fault) {
	record, fault := Validate(src)
	if fault != nil {
		return Staged{}, fault
	}
	name := filepath.Base(src)
	path := filepath.Join(dir, name)
	_, err := os.Stat(path)
	replaced := err == nil
	written, err := atomicfile.Copy(path, src, stagedMode, stagedDir)
	if err != nil {
		return Staged{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot stage %s as %s: %v", src, path, err)).WithCause(err)
	}
	record.Artifact = name
	record.Source = Local
	return Staged{Record: record, Path: path, Bytes: written, Replaced: replaced}, nil
}

// Clear removes every `.modl` staged in dir, in file-name order, and reports the
// file names it removed. A directory that does not exist has nothing to remove,
// which is the state before the first `module add`.
func Clear(dir string) ([]string, *contract.Fault) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			"cannot read the private module directory "+dir+": "+err.Error()).WithCause(err)
	}
	removed := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".modl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				fmt.Sprintf("cannot remove %s: %v", path, err)).WithCause(err)
		}
		removed = append(removed, entry.Name())
	}
	sort.Strings(removed)
	return removed, nil
}

// IDs lists the module ids the staged artifacts declare, in artifact order, which
// is the order `module list --private` reports them in.
func IDs(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		out = append(out, record.ID)
	}
	return out
}
