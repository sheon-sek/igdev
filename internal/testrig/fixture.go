package testrig

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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

// Snapshot is the set of paths present under the scratch root.
type Snapshot map[string]bool

// Snapshot records the current scratch tree.
func (e *Env) Snapshot() Snapshot {
	out := Snapshot{}
	_ = filepath.WalkDir(e.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if rel, relErr := filepath.Rel(e.Root, path); relErr == nil {
			out[rel] = d.IsDir()
		}
		return nil
	})
	return out
}

// Added lists paths that appeared since base, sorted.
func (e *Env) Added(base Snapshot) []string {
	var added []string
	for _, path := range e.Snapshot().paths() {
		if _, ok := base[path]; !ok {
			added = append(added, path)
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

// AssertNoLeaks fails when the run left anything behind in the scratch tree, in
// TMPDIR, or as a half-written temp file. This is the no-litter rule from the
// spec, enforced per run against the tree as it stood at launch.
func (r Result) AssertNoLeaks(t *testing.T) {
	t.Helper()
	if r.env == nil {
		t.Fatalf("Result was not produced by Env.Run")
	}
	r.env.AssertNoLeaks(t, r.before)
	r.env.AssertNoPartialWrites(t)
}

// AssertNoLeaks fails when a run left anything behind in the scratch tree beyond
// base, or anything at all in TMPDIR.
func (e *Env) AssertNoLeaks(t *testing.T, base Snapshot) {
	t.Helper()
	if added := e.Added(base); len(added) > 0 {
		t.Errorf("unexpected files left in scratch tree: %s", strings.Join(added, ", "))
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
