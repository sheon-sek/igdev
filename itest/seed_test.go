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
	testrig.WantExit(t, stale, contract.ExitFailure)
	envelope := testrig.Envelope(t, stale.Stdout)
	testrig.WantCode(t, envelope, contract.CodeSetupStale)
	if !strings.Contains(envelope.Message, "project seed") {
		t.Errorf("the stale reason does not name the seed: %q", envelope.Message)
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

// A secret in a tracked seed fails setup with the file and the field named, and
// writes no Setup Stamp.
func TestSetupRefusesASecretInTheSeed(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", seededContract)
	env.Write("repo/tests/seed/ignition/database-connection/hist/config.json", `{"settings":{"password":"hunter2"}}`)
	res := env.RunIn(dir, "setup", "--accept-eula", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConfigInvalid)
	if !strings.Contains(envelope.Message, "tests/seed/ignition/database-connection/hist/config.json") ||
		!strings.Contains(envelope.Message, ".settings.password") {
		t.Errorf("the refusal does not name the path and field: %q", envelope.Message)
	}
	if strings.Contains(res.Stdout+res.Stderr, "hunter2") {
		t.Error("the refusal repeats the secret")
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir, "setup.json")); !os.IsNotExist(err) {
		t.Errorf("a refused setup wrote the Setup Stamp: %v", err)
	}
}
