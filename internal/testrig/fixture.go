package testrig

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
)

// MinimalContract is the smallest valid Project Contract a fixture can carry.
const MinimalContract = `schema = 1

[project]
name = "fixture"
`

// Path resolves a scratch-relative path to an absolute one.
func (e *Env) Path(rel ...string) string {
	return filepath.Join(append([]string{e.Root}, rel...)...)
}

// abs accepts either convention: an absolute path passes through, a relative one
// is resolved under the scratch root so a test can never write into the repo.
func (e *Env) abs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return e.Path(path)
}

// Write creates (with parents) a file under the scratch root and returns its
// absolute path.
func (e *Env) Write(rel, content string) string {
	e.T.Helper()
	full := e.Path(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		e.T.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		e.T.Fatalf("write %s: %v", rel, err)
	}
	return full
}

// Mkdir creates a directory under the scratch root and returns its absolute
// path.
func (e *Env) Mkdir(rel string) string {
	e.T.Helper()
	full := e.Path(rel)
	if err := os.MkdirAll(full, 0o755); err != nil {
		e.T.Fatalf("mkdir %s: %v", rel, err)
	}
	return full
}

// Project creates a directory holding an igdev.toml Project Contract and returns
// its absolute path. An empty contract creates a bare directory: the
// outside-project case.
func (e *Env) Project(rel, contract string) string {
	e.T.Helper()
	dir := e.Mkdir(rel)
	if contract != "" {
		if err := os.WriteFile(filepath.Join(dir, "igdev.toml"), []byte(contract), 0o644); err != nil {
			e.T.Fatalf("write contract: %v", err)
		}
	}
	return dir
}

// SetupRecord writes a Checkout Setup record inside a project's .igdev/
// directory. Ticket 01 only asserts existence; ticket 02 validates the content.
// projectDir may be absolute or scratch-relative.
func (e *Env) SetupRecord(projectDir, content string) string {
	e.T.Helper()
	path := filepath.Join(e.abs(projectDir), ".igdev", "setup.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.T.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.T.Fatalf("write setup record: %v", err)
	}
	return path
}

// SetupStamp writes a Checkout Setup record whose Setup Stamp matches the
// Project Contract at contractPath, and returns the record's path. It stands in
// for `igdev setup`, which owns the real writer in ticket 09; ticket 08 needs the
// shape to exercise the Gate against a current checkout.
func (e *Env) SetupStamp(projectDir, contractPath string) string {
	e.T.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		e.T.Fatalf("read contract for its digest: %v", err)
	}
	stamp := gate.Stamp{
		Schema:         gate.StampSchema,
		ContractDigest: project.Digest(raw),
		ContractSchema: project.DeclaredSchema(raw),
		CLIContract:    contract.Version,
	}
	encoded, err := stamp.Encode()
	if err != nil {
		e.T.Fatalf("encode setup stamp: %v", err)
	}
	return e.SetupRecord(projectDir, string(encoded))
}

// LocalConfig writes the checkout-local config tier, .igdev/local.toml.
// projectDir may be absolute or scratch-relative.
func (e *Env) LocalConfig(projectDir, content string) string {
	e.T.Helper()
	path := filepath.Join(e.abs(projectDir), ".igdev", "local.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.T.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.T.Fatalf("write local config: %v", err)
	}
	return path
}

// Snapshot is the content of the scratch tree: every path mapped to a
// fingerprint — a directory marker, or the file's mode plus a hash of its bytes.
// A set of paths cannot see a run that rewrote or deleted something, and "zero
// leaks" has to mean both.
type Snapshot map[string]string

const dirMarker = "dir"

// Snapshot records the current scratch tree.
func (e *Env) Snapshot() Snapshot {
	out := Snapshot{}
	_ = filepath.WalkDir(e.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(e.Root, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			out[rel] = dirMarker
			return nil
		}
		out[rel] = fingerprint(path, d)
		return nil
	})
	return out
}

// fingerprint identifies a file by permission bits and content.
func fingerprint(path string, d fs.DirEntry) string {
	info, err := d.Info()
	if err != nil {
		return "unreadable"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("file:%o:unreadable", info.Mode().Perm())
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("file:%o:%x", info.Mode().Perm(), sum[:12])
}

// Change is one difference between two snapshots.
type Change struct {
	Kind string // "added", "modified", or "removed"
	Path string
}

// Changes lists how the tree differs from base, sorted by path.
func (e *Env) Changes(base Snapshot) []Change {
	now := e.Snapshot()
	var changes []Change
	for path, fp := range now {
		before, existed := base[path]
		switch {
		case !existed:
			changes = append(changes, Change{Kind: "added", Path: path})
		case before != fp:
			changes = append(changes, Change{Kind: "modified", Path: path})
		}
	}
	for path := range base {
		if _, still := now[path]; !still {
			changes = append(changes, Change{Kind: "removed", Path: path})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path != changes[j].Path {
			return changes[i].Path < changes[j].Path
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

// Added lists paths that appeared since base, sorted.
func (e *Env) Added(base Snapshot) []string {
	var added []string
	for _, c := range e.Changes(base) {
		if c.Kind == "added" {
			added = append(added, c.Path)
		}
	}
	return added
}

func (s Snapshot) paths() []string {
	out := make([]string, 0, len(s))
	for path := range s {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// formatChanges renders a change list for failure messages.
func formatChanges(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, c.Kind+" "+c.Path)
	}
	return strings.Join(parts, ", ")
}

// AssertNoLeaks fails when the run changed the scratch tree at all — nothing may
// be added, modified, or removed — or left anything in TMPDIR. This is the
// no-litter rule from the spec, enforced per run against the tree at launch.
func (r Result) AssertNoLeaks(t *testing.T) {
	t.Helper()
	if r.env == nil {
		t.Fatalf("Result was not produced by Env.Run")
	}
	r.env.AssertNoLeaks(t, r.before)
	r.env.AssertNoPartialWrites(t)
}

// AssertNoLeaks fails when anything in the scratch tree differs from base, or
// TMPDIR holds something.
func (e *Env) AssertNoLeaks(t *testing.T, base Snapshot) {
	t.Helper()
	if changes := e.Changes(base); len(changes) > 0 {
		t.Errorf("run changed the scratch tree: %s", formatChanges(changes))
	}
	e.AssertTempDirEmpty(t)
}

// AssertTempDirEmpty fails when TMPDIR holds anything: igdev must clean up every
// temp file it creates, including on failure paths.
func (e *Env) AssertTempDirEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(e.Temp)
	if err != nil {
		t.Fatalf("read TMPDIR %s: %v", e.Temp, err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("leaked %d temp file(s) in TMPDIR: %s", len(names), strings.Join(names, ", "))
	}
}

// AssertNoPartialWrites fails when any path under the scratch root still carries
// a temp-file marker, which is how a half-written atomic rename shows up.
func (e *Env) AssertNoPartialWrites(t *testing.T) {
	t.Helper()
	for _, path := range e.Snapshot().paths() {
		if strings.Contains(filepath.Base(path), ".tmp") {
			t.Errorf("temp-file leak: %s", path)
		}
	}
}

// Tree returns the scratch tree as a sorted path list, for failure messages.
func (e *Env) Tree() string {
	return strings.Join(e.Snapshot().paths(), "\n")
}
