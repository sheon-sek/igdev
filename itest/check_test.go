package itest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/jython"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The check pipeline and the verbs that dispatch the project's declared stages:
// the frozen stage order, stop-on-failure, an undeclared stage skipped rather
// than failed, a declared stage's exit code propagated, and `verify --gateway`
// composing the runtime half.

// pipelineStageState is one stage as the envelope reports it.
type pipelineStageState struct {
	Stage       string              `json:"stage"`
	Status      string              `json:"status"`
	Message     string              `json:"message"`
	Enabled     []string            `json:"enabled"`
	Missing     []string            `json:"missing"`
	Paths       []string            `json:"paths"`
	Checked     int                 `json:"checked"`
	Findings    int                 `json:"findings"`
	Command     string              `json:"command"`
	Exit        int                 `json:"exit"`
	Jar         string              `json:"jar"`
	FileCount   int                 `json:"file_count"`
	Diagnostics []jython.Diagnostic `json:"diagnostics"`
	ModulesDir  string              `json:"modules_dir"`
	Staged      []string            `json:"staged"`
	RuntimeDir  string              `json:"runtime_dir"`
	Artifacts   []struct {
		Glob       string   `json:"glob"`
		Source     string   `json:"source"`
		Artifact   string   `json:"artifact"`
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Version    string   `json:"version"`
		Action     string   `json:"action"`
		Superseded []string `json:"superseded"`
	} `json:"artifacts"`
}

// pipelineState is the `data` member of check, test, build, and verify.
type pipelineState struct {
	Stages     []pipelineStageState `json:"stages"`
	Failed     string               `json:"failed"`
	GatewayURL string               `json:"gateway_url"`
}

func pipelineOf(t *testing.T, stdout string) pipelineState {
	t.Helper()
	var data pipelineState
	testrig.DataOf(t, stdout, &data)
	return data
}

// stageNamed returns the staged entry with a name, failing when the run never
// reached it.
func stageNamed(t *testing.T, data pipelineState, name string) pipelineStageState {
	t.Helper()
	for _, stage := range data.Stages {
		if stage.Stage == name {
			return stage
		}
	}
	t.Fatalf("the run has no %s stage: %+v", name, data.Stages)
	return pipelineStageState{}
}

// stageNames lists the stages a run reached, in order.
func stageNames(data pipelineState) []string {
	out := make([]string, 0, len(data.Stages))
	for _, stage := range data.Stages {
		out = append(out, stage.Stage)
	}
	return out
}

// pipelineContract renders the fixture contract from the frozen defaults, so a
// test only states what it is about.
func pipelineContract(mutate func(*project.Doc)) string {
	doc := project.DefaultDoc()
	doc.Project.Name = "fixture"
	doc.Scan.Jython = []string{"src"}
	doc.Scan.Capabilities = []string{"src"}
	if mutate != nil {
		mutate(&doc)
	}
	return string(doc.Render())
}

// pipelineFixture materializes one Instance with a current Checkout Setup, which
// every pipeline verb needs: validating the enabled modules compares them against
// what this checkout stages.
func pipelineFixture(t *testing.T, env *testrig.Env, mutate func(*project.Doc)) string {
	t.Helper()
	dir := env.Project("repo", pipelineContract(mutate))
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	return dir
}

// check runs the four frozen stages in order and reports each one.
func TestCheckRunsTheFrozenPipeline(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Commands.Check = "printf 'check stage ran\n'"
	})
	env.Write("repo/src/uses.py", "value = system.tag.readBlocking([\"[default]x\"])\n")

	res := env.RunIn(dir, "check", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "check_passed.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	want := []string{"module-validate", "module-scan", "declared-check", "jython-check"}
	if got := stageNames(data); len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("stages = %v, want %v", got, want)
			}
		}
	}
	for _, stage := range data.Stages {
		if stage.Status != "passed" {
			t.Errorf("stage %s = %q, want passed", stage.Stage, stage.Status)
		}
	}
	if data.Failed != "" {
		t.Errorf("failed = %q, want empty on a passing run", data.Failed)
	}
	if scan := stageNamed(t, data, "module-scan"); scan.Checked != 1 {
		t.Errorf("module-scan checked %d capability reference(s), want 1", scan.Checked)
	}
	if jythonStage := stageNamed(t, data, "jython-check"); jythonStage.FileCount != 1 {
		t.Errorf("jython-check compiled %d file(s), want 1", jythonStage.FileCount)
	}
	// In machine mode a child's own output is progress: on stderr, never mixed
	// into the envelope.
	if !strings.Contains(res.Stderr, "check stage ran") {
		t.Errorf("the declared stage's output is missing from stderr:\n%s", res.Stderr)
	}
	testrig.Envelope(t, res.Stdout)

	human := env.RunIn(dir, "check")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "check_passed.txt", human.Stdout)

	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A module the whitelist names that nothing can load stops the run at the first
// stage: the later stages never run.
func TestCheckStopsAtModuleValidation(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Modules.Enabled = []string{"com.acme.ghost"}
	})
	env.Write("repo/src/uses.py", "value = 1\n")

	res := env.RunIn(dir, "check", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleArtifactMissing)
	env.Golden(t, "check_validate_failed.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	if got := stageNames(data); len(got) != 1 || got[0] != "module-validate" {
		t.Errorf("stages = %v, want module-validate alone", got)
	}
	if data.Failed != "module-validate" {
		t.Errorf("failed = %q, want module-validate", data.Failed)
	}
	if missing := stageNamed(t, data, "module-validate").Missing; len(missing) != 1 || missing[0] != "com.acme.ghost" {
		t.Errorf("missing = %v, want the whitelisted ghost module", missing)
	}
	if len(env.JavaCalls(t)) != 0 {
		t.Error("a stopped pipeline launched a JVM")
	}
}

// A capability nothing owns stops the run at the scan stage.
func TestCheckStopsAtTheScanStage(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, nil)
	env.Write("repo/src/uses.py", "value = system.nonexistent.api()\n")

	res := env.RunIn(dir, "check", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUnknownCapability)
	env.Golden(t, "check_scan_failed.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	want := []string{"module-validate", "module-scan"}
	got := stageNames(data)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("stages = %v, want %v", got, want)
	}
	if data.Failed != "module-scan" {
		t.Errorf("failed = %q, want module-scan", data.Failed)
	}
	if len(env.JavaCalls(t)) != 0 {
		t.Error("a stopped pipeline launched a JVM")
	}
}

// A declared stage that exits non-zero propagates its own exit code and stops the
// pipeline before the Jython stage.
func TestCheckPropagatesTheDeclaredStageExit(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Commands.Check = "printf 'child says hi\n'; exit 7"
	})
	env.Write("repo/src/uses.py", "value = 1\n")

	res := env.RunIn(dir, "check", "--json")
	testrig.WantExit(t, res, contract.Exit(7))
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeCommandFailed)
	env.Golden(t, "check_command_failed.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	want := []string{"module-validate", "module-scan", "declared-check"}
	got := stageNames(data)
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	stage := stageNamed(t, data, "declared-check")
	if stage.Status != "failed" || stage.Exit != 7 {
		t.Errorf("declared-check = %+v, want failed with exit 7", stage)
	}
	if data.Failed != "declared-check" {
		t.Errorf("failed = %q, want declared-check", data.Failed)
	}
	if len(env.JavaCalls(t)) != 0 {
		t.Error("a stopped pipeline launched a JVM")
	}
	if !strings.Contains(res.Stderr, "child says hi") {
		t.Errorf("the child's output is missing from stderr:\n%s", res.Stderr)
	}
}

// The declared stage runs at the Project Root and the contract's scan paths are
// repository-relative: a run from a subdirectory behaves like a run from the root.
func TestCheckRunsStagesAtTheProjectRoot(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Commands.Check = "test -f igdev.toml && printf 'at root\n'"
	})
	env.Write("repo/src/uses.py", "value = 1\n")
	sub := env.Mkdir("repo/sub")

	res := env.RunIn(sub, "check", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	data := pipelineOf(t, res.Stdout)
	if stage := stageNamed(t, data, "declared-check"); stage.Status != "passed" {
		t.Errorf("declared-check = %+v, want the stage to have found igdev.toml in its cwd", stage)
	}
	for _, name := range []string{"module-scan", "jython-check"} {
		stage := stageNamed(t, data, name)
		if len(stage.Paths) != 1 || stage.Paths[0] != filepath.Join(dir, "src") {
			t.Errorf("%s paths = %v, want the contract path resolved against the Project Root", name, stage.Paths)
		}
	}
}

// The Jython stage names every failing file, and reports them structurally.
func TestCheckFailsTheJythonStageWithDiagnostics(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, nil)
	env.Write("repo/src/good.py", "value = 1\n")
	env.Write("repo/src/bad.py", "value = 1\nother = 2\nbroken = )\n")

	res := env.RunIn(dir, "check", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJythonSyntax)
	env.Golden(t, "check_jython_failed.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	if data.Failed != "jython-check" {
		t.Errorf("failed = %q, want jython-check", data.Failed)
	}
	stage := stageNamed(t, data, "jython-check")
	if len(stage.Diagnostics) != 1 || stage.Diagnostics[0].Line != 3 {
		t.Errorf("diagnostics = %+v, want one failure at line 3", stage.Diagnostics)
	}
	if stage.FileCount != 2 {
		t.Errorf("file_count = %d, want 2 (all files are compiled, not just the first)", stage.FileCount)
	}
}

// An undeclared stage is skipped, never failed: a repository that declares no
// test command still passes `igdev test`.
func TestTestVerbDispatchesTheDeclaredStage(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Commands.Test = "printf 'tests ran\n'"
	})

	res := env.RunIn(dir, "test", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "test_stage.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	stage := stageNamed(t, data, "declared-test")
	if stage.Status != "passed" || stage.Command != "printf 'tests ran\n'" {
		t.Errorf("declared-test = %+v, want passed with the declared command", stage)
	}

	human := env.RunIn(dir, "test")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "test_stage.txt", human.Stdout)
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))

	// The same contract with the stage cleared: a skip, not a failure.
	undeclared := env.Project("undeclared", pipelineContract(nil))
	testrig.WantExit(t, env.RunIn(undeclared, "setup", "--accept-eula"), contract.ExitOK)
	skipped := env.RunIn(undeclared, "test")
	testrig.WantExit(t, skipped, contract.ExitOK)
	env.Golden(t, "test_skipped.txt", skipped.Stdout)
}

// build dispatches the declared stage and then re-stages the modules the Gateway
// mounts.
func TestBuildVerbRestagesModules(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Commands.Build = "printf 'built\n'"
	})
	artifact := modl(t, env, "downloads/com.acme.vision.modl", "<MODL>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", artifact), contract.ExitOK)

	res := env.RunIn(dir, "build", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "build_stage.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	got := stageNames(data)
	if len(got) != 2 || got[0] != "declared-build" || got[1] != "module-restage" {
		t.Fatalf("stages = %v, want declared-build then module-restage", got)
	}
	restage := stageNamed(t, data, "module-restage")
	if len(restage.Staged) != 1 || restage.Staged[0] != "com.acme.vision" {
		t.Errorf("staged = %v, want the added module", restage.Staged)
	}
	if restage.RuntimeDir == "" || !strings.HasPrefix(restage.ModulesDir, dir) {
		t.Errorf("module-restage = %+v, want the checkout's staging and runtime directories", restage)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir, "runtime", "compose.yaml")); err != nil {
		t.Errorf("the runtime files were not re-materialized: %v", err)
	}

	human := env.RunIn(dir, "build")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "build_stage.txt", human.Stdout)
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A module repository declares what its own build produces: build stages every
// match of [modules].artifacts without a manual `module add`, and a version bump
// replaces the previous build instead of leaving two artifacts for one module id.
func TestBuildStagesTheDeclaredArtifacts(t *testing.T) {
	env := testrig.NewEnv(t)
	modl(t, env, "repo/build-src/com.acme.vision-1.2.3.modl", "<MODL>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Modules.Artifacts = []string{"build/*.modl"}
		doc.Commands.Build = "mkdir -p build && cp build-src/*.modl build/"
	})
	staging := filepath.Join(dir, project.StateDir, "modules")

	res := env.RunIn(dir, "build", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "build_artifacts.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	restage := stageNamed(t, data, "module-restage")
	if len(restage.Artifacts) != 1 {
		t.Fatalf("module-restage staged %+v, want the one declared artifact", restage.Artifacts)
	}
	stagedArtifact := restage.Artifacts[0]
	if stagedArtifact.Glob != "build/*.modl" || stagedArtifact.ID != "com.acme.vision" ||
		stagedArtifact.Version != "1.2.3" || stagedArtifact.Action != "created" {
		t.Errorf("staged artifact = %+v, want the declared glob's match", stagedArtifact)
	}
	if got := strings.Join(stagedArtifacts(t, staging), ","); got != "com.acme.vision-1.2.3.modl" {
		t.Errorf("staging directory holds %q, want the build's artifact", got)
	}
	// Staging is not a contract change, so the Checkout Setup stays current.
	if state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); state.Setup.StampState != "current" {
		t.Errorf("stamp_state = %q, want current: staging is not a contract write", state.Setup.StampState)
	}

	// The bump: the same module id under a new file name. The old build must not
	// stay mounted beside the new one.
	if err := os.Remove(filepath.Join(dir, "build", "com.acme.vision-1.2.3.modl")); err != nil {
		t.Fatalf("remove the old build output: %v", err)
	}
	modl(t, env, "repo/build-src/com.acme.vision-1.2.4.modl", "<MODL_NEXT>", moduleXML("com.acme.vision", "Acme Vision", "1.2.4"))
	if err := os.Remove(env.Path("repo", "build-src", "com.acme.vision-1.2.3.modl")); err != nil {
		t.Fatalf("remove the old build source: %v", err)
	}

	bumped := env.RunIn(dir, "build", "--json")
	testrig.WantExit(t, bumped, contract.ExitOK)
	after := stageNamed(t, pipelineOf(t, bumped.Stdout), "module-restage")
	if len(after.Artifacts) != 1 || len(after.Artifacts[0].Superseded) != 1 ||
		after.Artifacts[0].Superseded[0] != "com.acme.vision-1.2.3.modl" {
		t.Errorf("the version bump superseded %+v, want the previous build", after.Artifacts)
	}
	if got := strings.Join(stagedArtifacts(t, staging), ","); got != "com.acme.vision-1.2.4.modl" {
		t.Errorf("staging directory holds %q, want one artifact per module id", got)
	}
	if got := strings.Join(after.Staged, ","); got != "com.acme.vision" {
		t.Errorf("staged ids = %q, want the module once", got)
	}
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A declared glob that matches nothing fails the build: the contract says what
// the build produces, and a run that found none of it staged nothing, which must
// not look like success.
func TestBuildFailsWhenADeclaredGlobMatchesNothing(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Modules.Artifacts = []string{"build/*.modl"}
		doc.Commands.Build = "printf 'built nothing\n'"
	})

	res := env.RunIn(dir, "build", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "build_artifact_missing.json", res.Stdout)

	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleArtifactMissing)
	testrig.WantRemediation(t, envelope, "igdev build")

	data := pipelineOf(t, res.Stdout)
	if data.Failed != "module-restage" {
		t.Errorf("failed = %q, want the stage that resolves the globs", data.Failed)
	}
	stage := stageNamed(t, data, "module-restage")
	if stage.Status != "failed" || !strings.Contains(stage.Message, "build/*.modl") {
		t.Errorf("module-restage = %+v, want a failure naming the glob", stage)
	}
	if got := stagedArtifacts(t, filepath.Join(dir, project.StateDir, "modules")); len(got) != 0 {
		t.Errorf("the failed build staged %v", got)
	}

	human := env.RunIn(dir, "build")
	testrig.WantExit(t, human, contract.ExitFailure)
	if !strings.Contains(human.Stderr, "build/*.modl") {
		t.Errorf("the human failure does not name the glob:\nstdout:\n%s\nstderr:\n%s", human.Stdout, human.Stderr)
	}
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// verify --gateway composes the runtime half: the Gateway starts, is waited for,
// and is smoke checked, and it is left running so the URL stays usable.
func TestVerifyGatewayRunsTheRuntimeHalf(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	jythonHarness(t, env)
	dir := pipelineFixture(t, env, func(doc *project.Doc) {
		doc.Gateway.SmokeEndpoints = []string{"/system/gateway/info"}
	})
	env.Write("repo/src/uses.py", "value = 1\n")
	stamp := pinPorts(t, env, dir, freeTriplet(t))
	normalizeInstance(t, env, stamp)
	normalizeConsent(t, env)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{
		"/":                    200,
		"/system/gateway/info": 200,
	})

	res := env.RunIn(dir, "verify", "--gateway", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "verify_gateway.json", res.Stdout)

	data := pipelineOf(t, res.Stdout)
	want := []string{
		"module-validate", "module-scan", "declared-check", "jython-check",
		"declared-test", "declared-build", "module-restage",
		"gateway-up", "gateway-wait", "gateway-smoke",
	}
	got := stageNames(data)
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stages = %v, want %v", got, want)
		}
	}
	if wantURL := fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP); data.GatewayURL != wantURL {
		t.Errorf("gateway_url = %q, want the recorded %q", data.GatewayURL, wantURL)
	}
	if stub.Hits() == 0 {
		t.Error("verify --gateway never probed the Gateway")
	}
	// The Gateway is left running: the run must not have stopped it.
	for _, call := range env.DockerCalls(t) {
		if testrig.ComposeVerb(call.Argv) == "down" {
			t.Errorf("verify --gateway stopped the Gateway it just started: %v", call.Argv)
		}
	}
	// The run left the Gateway up; stopping it is the caller's step, and the
	// test does it so the hygiene gate still sees a clean machine.
	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}
