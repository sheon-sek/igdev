package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// status outside a Project Root is the safe first call an agent makes: ok true,
// initialized false, no error code.
func TestStatusOutsideProjectJSON(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	projectDir := env.Project("plain", "")

	res := env.RunIn(projectDir, "status", "--json")

	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "status_outside_project.json", res.Stdout)
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)

	data := testrig.Status(t, res.Stdout)
	if data.Initialized {
		t.Errorf("initialized = true outside a Project Root")
	}
	if data.ProjectRoot != "" {
		t.Errorf("project_root = %q, want empty", data.ProjectRoot)
	}
}

// status inside a project discovers the Project Root by searching upward, and
// reports the raw contract facts: presence and declared schema version.
func TestStatusInsideProjectJSON(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.Project("repo", `schema = 1

[project]
name = "fixture"
ignition_version = "8.1.21"
`)
	nested := env.Mkdir("repo/src/main/python")

	res := env.RunIn(nested, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "status_inside_project.json", res.Stdout)
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)

	data := testrig.Status(t, res.Stdout)
	if !data.Initialized {
		t.Fatalf("initialized = false inside a project")
	}
	if got := env.Normalize(data.ProjectRoot); got != "<ROOT>/repo" {
		t.Errorf("project_root = %s, want the directory holding igdev.toml", got)
	}
	if !data.Contract.Present {
		t.Errorf("contract.present = false, want true")
	}
	if data.Contract.SchemaVersion != 1 {
		t.Errorf("contract.schema_version = %d, want 1", data.Contract.SchemaVersion)
	}
	if data.Setup.Present {
		t.Errorf("setup.present = true before any setup ran")
	}
}

// A Checkout Setup record is reported as present by existence alone in ticket
// 01; stamp validation belongs to ticket 02.
func TestStatusReportsSetupPresence(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", testrig.MinimalContract)
	env.SetupRecord(root, `{"instance_id":"not-yet-validated"}`)

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	data := testrig.Status(t, res.Stdout)
	if !data.Setup.Present {
		t.Fatalf("setup.present = false with .igdev/setup.json in place\n%s", env.Tree())
	}
	if got := env.Normalize(data.Setup.Path); got != "<ROOT>/repo/.igdev/setup.json" {
		t.Errorf("setup.path = %s, want the .igdev/setup.json path", got)
	}
}

// A contract with no declared schema version reports 0 rather than guessing.
func TestStatusContractWithoutSchemaKey(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "[project]\nname = \"no-schema-declared\"\n")

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	data := testrig.Status(t, res.Stdout)
	if data.Contract.SchemaVersion != 0 {
		t.Errorf("contract.schema_version = %d, want 0 when the key is absent", data.Contract.SchemaVersion)
	}
	if !data.Initialized {
		t.Errorf("initialized = false with a contract present")
	}
}

// Human mode prints the same facts as prose and touches nothing.
func TestStatusHumanOutput(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", testrig.MinimalContract)

	res := env.RunIn(root, "status")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "status_inside_project.txt", res.Stdout)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty (no network, no notice)", res.Stderr)
	}
	res.AssertNoLeaks(t)
}

// Contract files that do not parse are a config-tier failure, not a crash: the
// envelope names the file and the tier.
func TestStatusMalformedContractFailsClosed(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n[project\nname = broken\n")

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeConfigInvalid)
	if !strings.Contains(res.Stdout, "igdev.toml") {
		t.Errorf("envelope does not name the offending file:\n%s", res.Stdout)
	}
	res.AssertNoLeaks(t)
}
