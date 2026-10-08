package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

const seededContract = testrig.MinimalContract + `
[gateway]
seed = ["tests/seed"]
`

// setup copies the tracked seed into the image's build context, a seed change
// makes the checkout stale, and a resource dropped from the seed leaves the
// build context again.
func TestSetupMergesTheProjectSeed(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", seededContract)
	env.Write("repo/tests/seed/ignition/tag-provider/sim/config.json", `{"profile":{"type":"STANDARD"}}`+"\n")
	env.Write("repo/tests/seed/ignition/tag-provider/sim/resource.json", `{"scope":"A","files":["config.json"]}`+"\n")
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)

	external := filepath.Join(dir, project.StateDir, "runtime", "seed", "config", "resources", "external")
	copied, err := os.ReadFile(filepath.Join(external, "ignition", "tag-provider", "sim", "config.json"))
	if err != nil || !strings.Contains(string(copied), "STANDARD") {
		t.Fatalf("setup did not copy the seed into the build context: %v %q", err, copied)
	}
	if _, err := os.Stat(filepath.Join(external, "ignition", "api-token", "igdev", "config.json")); err != nil {
		t.Errorf("igdev's own seed is gone next to the project seed: %v", err)
	}

	env.Write("repo/tests/seed/ignition/tag-provider/sim/config.json", `{"profile":{"type":"MANUAL"}}`+"\n")
	stale := env.RunIn(dir, "gateway", "url", "--json")
	testrig.WantExit(t, stale, contract.ExitOK)
	if !strings.Contains(stale.Stderr, "refreshing the Checkout Setup first") || !strings.Contains(stale.Stderr, "project seed") {
		t.Errorf("the refresh note does not name the seed:\n%s", stale.Stderr)
	}
	if copied, _ := os.ReadFile(filepath.Join(external, "ignition", "tag-provider", "sim", "config.json")); !strings.Contains(string(copied), "MANUAL") {
		t.Errorf("the refresh did not copy the changed seed: %q", copied)
	}

	if err := os.RemoveAll(env.Path("repo/tests/seed/ignition/tag-provider/sim")); err != nil {
		t.Fatal(err)
	}
	env.Write("repo/tests/seed/ignition/tag-group/fast/config.json", `{"settings":{"rate":250}}`+"\n")
	testrig.WantExit(t, env.RunIn(dir, "setup"), contract.ExitOK)
	if _, err := os.Stat(filepath.Join(external, "ignition", "tag-provider", "sim", "config.json")); !os.IsNotExist(err) {
		t.Errorf("a resource dropped from the seed is still in the build context: %v", err)
	}
	testrig.WantExit(t, env.RunIn(dir, "gateway", "url"), contract.ExitOK)
}

// A secret in a tracked seed is a warning naming the file and the field, never a
// refusal, and the warning does not repeat the secret (igdev#104).
func TestSetupWarnsAboutASecretInTheSeed(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", seededContract)
	env.Write("repo/tests/seed/ignition/database-connection/hist/config.json", `{"settings":{"password":"hunter2"}}`)
	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stderr, "tests/seed/ignition/database-connection/hist/config.json") ||
		!strings.Contains(res.Stderr, ".settings.password") {
		t.Errorf("the warning does not name the path and field:\n%s", res.Stderr)
	}
	if strings.Contains(res.Stdout+res.Stderr, "hunter2") {
		t.Error("the warning repeats the secret")
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir, "runtime", "seed", "config", "resources", "external",
		"ignition", "database-connection", "hist", "config.json")); err != nil {
		t.Errorf("the seeded file is not in the build context: %v", err)
	}
}
