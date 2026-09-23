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

// Artifact is one artifact a `[modules].artifacts` glob resolved and igdev
// staged, with the glob that produced it.
type Artifact struct {
	// Glob is the contract pattern that matched Source.
	Glob string
	// Source is the artifact the glob matched, inside the repository.
	Source string
	// Staged is what the copy did: the metadata the artifact declares, the staged
	// path, its size, and whether an artifact of the same file name was replaced.
	Staged Staged
	// Superseded lists the staged artifact file names this one took the place of:
	// files that declared the same module id under another name, removed so one
	// module id stays one staged file. A version bump changes the file name, so
	// without this the old build would stay mounted beside the new one.
	Superseded []string
}

// StageArtifacts resolves the contract's artifact globs against the Project Root,
// in declaration order, and stages every match the way `module add` stages one
// file.
//
// A glob that matches nothing is a fault: the contract declares what the build
// produces, so a build that produced none of it has nothing for the Gateway to
// load and silence would look like success. Staging an artifact also removes any
// other staged file that declares the same module id, which is what makes a
// version bump replace the previous build instead of joining it.
func StageArtifacts(root, dir string, globs []string) ([]Artifact, *contract.Fault) {
	if len(globs) == 0 {
		return nil, nil
	}
	existing, fault := scanFiles(dir)
	if fault != nil {
		return nil, fault
	}
	staged := make(map[string]string, len(existing))
	for _, record := range existing {
		if record.ID != "" {
			staged[record.Artifact] = record.ID
		}
	}

	var out []Artifact
	for _, pattern := range globs {
		matches, fault := globArtifacts(root, pattern)
		if fault != nil {
			return nil, fault
		}
		if len(matches) == 0 {
			return nil, artifactGlobFault(pattern)
		}
		for _, match := range matches {
			written, fault := Stage(dir, match)
			if fault != nil {
				return nil, fault
			}
			artifact := Artifact{Staged: written, Glob: pattern, Source: match}
			for name, id := range staged {
				if id != written.ID || name == written.Artifact {
					continue
				}
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
					return nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot remove the superseded artifact %s: %v", name, err)).WithCause(err)
				}
				artifact.Superseded = append(artifact.Superseded, name)
				delete(staged, name)
			}
			sort.Strings(artifact.Superseded)
			staged[written.Artifact] = written.ID
			out = append(out, artifact)
		}
	}
	return out, nil
}

// globArtifacts resolves one repository-relative artifact glob against the
// Project Root. Directories never match: a glob names build outputs, and a
// directory is not one.
func globArtifacts(root, pattern string) ([]string, *contract.Fault) {
	path := pattern
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, pattern)
	}
	matches, err := filepath.Glob(path)
	if err != nil {
		return nil, contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
			fmt.Sprintf("[modules].artifacts entry %q is not a glob igdev can resolve: %v", pattern, err)).
			WithRemediation(contract.Remediation{
				Command: "igdev init",
				Why:     "rewrite the contract's artifact globs (the diff shows what changes)",
			})
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, err := os.Stat(match); err != nil || info.IsDir() {
			continue
		}
		out = append(out, match)
	}
	return out, nil
}

// artifactGlobFault is the refusal a build returns when a contract glob resolved
// to nothing: the artifact the project declared it produces is not there.
func artifactGlobFault(pattern string) *contract.Fault {
	return contract.NewFault(contract.CodeModuleArtifactMissing, contract.ExitFailure,
		fmt.Sprintf("[modules].artifacts glob %q matched no module artifact in the Project Root", pattern)).
		WithRemediation(
			contract.Remediation{
				Command: "igdev build",
				Why:     "run the project's build stage, which produces the declared artifacts",
			},
			contract.Remediation{
				Command: "igdev init --modules-artifacts ''",
				Why:     "clear the globs when the project no longer produces a staged artifact",
			})
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
