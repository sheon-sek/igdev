package itest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

type cleanupResult struct {
	Scope   string `json:"scope"`
	DryRun  bool   `json:"dry_run"`
	Removed int    `json:"removed"`
	Pending int    `json:"would_remove"`
	Failed  int    `json:"failed"`
	Items   []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		Action string `json:"action"`
	} `json:"items"`
}

func (r cleanupResult) has(kind, ref, action string) bool {
	for _, item := range r.Items {
		if item.Kind == kind && item.Ref == ref && item.Action == action {
			return true
		}
	}
	return false
}

// A started Gateway and its Checkout Setup leave nothing behind, and a second
// run finds nothing to do.
func TestCleanupRemovesTheInstanceAndSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	dry := env.RunIn(dir, "cleanup", "--dry-run", "--json")
	testrig.WantExit(t, dry, contract.ExitOK)
	var plan cleanupResult
	testrig.DataOf(t, dry.Stdout, &plan)
	if !plan.DryRun || plan.Pending != 3 || plan.Removed != 0 {
		t.Fatalf("dry run = %+v, want 3 items pending and none removed", plan)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); err != nil {
		t.Fatalf("a dry run removed .igdev/: %v", err)
	}

	res := env.RunIn(dir, "cleanup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data cleanupResult
	testrig.DataOf(t, res.Stdout, &data)
	if data.Scope != "checkout" || data.Removed != 3 || data.Failed != 0 {
		t.Fatalf("cleanup = %+v", data)
	}
	if !data.has("container", testrig.ComposeContainer(stamp.Namespace()), "removed") ||
		!data.has("volume", testrig.ComposeVolume(stamp.Namespace()), "removed") {
		t.Fatalf("the Instance's container and volume were not reported removed: %+v", data.Items)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); !os.IsNotExist(err) {
		t.Fatalf(".igdev/ survived cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, project.ContractFile)); err != nil {
		t.Fatalf("cleanup without --deinit removed igdev.toml: %v", err)
	}
	env.AssertNoDockerOrphans(t)

	again := env.RunIn(dir, "cleanup", "--json")
	testrig.WantExit(t, again, contract.ExitOK)
	var second cleanupResult
	testrig.DataOf(t, again.Stdout, &second)
	if second.Removed != 0 || second.Pending != 0 {
		t.Fatalf("second cleanup still found work: %+v", second)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); !os.IsNotExist(err) {
		t.Fatal("cleanup re-created the Checkout Setup it had removed")
	}
}

// --deinit takes igdev.toml and the AGENTS.md block, and leaves .gitignore.
func TestCleanupDeinit(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "init", "--yes"), contract.ExitOK)
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	res := env.RunIn(dir, "cleanup", "--deinit", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	for _, name := range []string{project.ContractFile, "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived --deinit", name)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || string(after) != string(gitignore) {
		t.Errorf(".gitignore changed: %q, was %q", after, gitignore)
	}
}

// --machine reaches beyond the checkout, so without --yes a non-terminal run
// stops at exit 2 with the plan and removes nothing.
func TestCleanupMachineNeedsYes(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	res := env.RunIn(dir, "cleanup", "--machine", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUsage)
	testrig.WantRemediation(t, envelope, "igdev cleanup --machine --yes")
	var plan cleanupResult
	testrig.DataOf(t, res.Stdout, &plan)
	if !plan.DryRun || !plan.has("container", testrig.ComposeContainer(stamp.Namespace()), "would_remove") {
		t.Fatalf("the refusal must carry the plan: %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); err != nil {
		t.Fatal("an unconfirmed --machine removed .igdev/")
	}

	res = env.RunIn(dir, "cleanup", "--machine", "--yes", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}
