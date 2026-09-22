package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/testrig"
)

// The scoped leak assertion is what keeps "setup writes only into .igdev/ and the
// Consent record" checkable without giving up the no-litter rule: writes inside
// the allowed roots are expected, anything else is still a leak.
func TestRigScopedLeakAssertion(t *testing.T) {
	env := testrig.NewEnv(t)
	env.Write("repo/igdev.toml", "schema = 1\n")
	base := env.Snapshot()

	// Expected writes: the Checkout Setup inside the fixture, the Consent record
	// under the scratch HOME.
	env.Write("repo/.igdev/setup.json", "{}\n")
	env.Write("home/.config/igdev/accepted.toml", "[ignition-eula]\n")
	if changes := env.ChangesOutside(base, env.Path("repo"), env.Home); len(changes) != 0 {
		t.Errorf("allowed roots were reported as leaks: %v", changes)
	}

	// A write anywhere else is still caught: here at the scratch root, next to the
	// fixture, where neither root reaches.
	env.Write("stray.txt", "leak\n")
	env.Write("repo/.igdev/runtime/compose.yaml", "still inside the fixture\n")
	outside := env.ChangesOutside(base, env.Path("repo"), env.Home)
	if len(outside) != 1 || outside[0].Path != "stray.txt" {
		t.Errorf("ChangesOutside = %v, want exactly the stray file", outside)
	}
	// A scratch-relative root is accepted too, so a test never has to absolutize.
	if changes := env.ChangesOutside(base, "repo/.igdev", "home"); len(changes) != 1 {
		t.Errorf("ChangesOutside with relative roots = %v, want exactly the stray file", changes)
	}
}

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
