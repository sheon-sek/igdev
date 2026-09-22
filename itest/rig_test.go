package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/testrig"
)

// The leak assertion is only worth having if it sees more than new files: a run
// that rewrote or deleted something in the checkout is exactly the damage the
// no-litter rule is about.
func TestRigDetectsEveryFilesystemChange(t *testing.T) {
	env := testrig.NewEnv(t)
	env.Write("repo/keep.txt", "untouched\n")
	env.Write("repo/rewrite.txt", "original\n")
	env.Write("repo/doomed.txt", "gone\n")
	base := env.Snapshot()

	if changes := env.Changes(base); len(changes) != 0 {
		t.Fatalf("an untouched tree reports changes: %v", changes)
	}

	env.Write("repo/rewrite.txt", "original plus a word\n")
	env.Write("repo/added.txt", "new\n")
	if err := os.Remove(env.Path("repo/doomed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	seen := map[string]string{}
	for _, c := range env.Changes(base) {
		seen[filepath.Base(c.Path)] = c.Kind
	}
	for name, want := range map[string]string{
		"rewrite.txt": "modified",
		"added.txt":   "added",
		"doomed.txt":  "removed",
	} {
		if got := seen[name]; got != want {
			t.Errorf("%s reported as %q, want %q (full change set: %v)", name, got, want, keys(seen))
		}
	}
	// An untouched file must not be reported.
	if _, ok := seen["keep.txt"]; ok {
		t.Errorf("keep.txt reported as changed")
	}
}

// A mode change is a change too: a run that chmods the contract is visible.
func TestRigDetectsModeChange(t *testing.T) {
	env := testrig.NewEnv(t)
	path := env.Write("repo/igdev.toml", "schema = 1\n")
	base := env.Snapshot()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	var found bool
	for _, c := range env.Changes(base) {
		if c.Kind == "modified" && strings.HasSuffix(c.Path, "igdev.toml") {
			found = true
		}
	}
	if !found {
		t.Errorf("a permission change went unnoticed: %v", env.Changes(base))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
