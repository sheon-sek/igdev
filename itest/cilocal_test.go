package itest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// `igdev ci-local` runs the project's GitHub Actions workflows locally through
// act, from the Project Root. These tests assert the whole observable behaviour
// through seam S1 — the exact argv act receives, the directory it runs in, the
// exit code that comes back, the streaming in both dialects — and the faults the
// verb reports instead: no act, no --job, no current Checkout Setup.

// ciLocalState is the `data` member of a ci-local envelope.
type ciLocalState struct {
	Event      string   `json:"event"`
	Job        string   `json:"job"`
	Offline    bool     `json:"offline"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	Workdir    string   `json:"workdir"`
	Exit       int      `json:"exit"`
	OutputTail []string `json:"output_tail"`
}

func ciLocalData(t *testing.T, stdout string) ciLocalState {
	t.Helper()
	var data ciLocalState
	testrig.DataOf(t, stdout, &data)
	return data
}

// ciLocalFixture materializes one Instance the way `igdev setup` does, which is
// the state every ci-local run needs: a current Checkout Setup. The docker shim
// is installed so "ci-local never touches the container engine" is an assertion
// about a log that would exist if it did.
func ciLocalFixture(t *testing.T, env *testrig.Env) string {
	t.Helper()
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	env.RegisterReplacement(dir, "<ROOT_REPO>")
	return dir
}

// onlyCall is the single act invocation a run must have made.
func onlyCall(t *testing.T, env *testrig.Env) testrig.ActCall {
	t.Helper()
	calls := env.ActCalls(t)
	if len(calls) != 1 {
		t.Fatalf("act was invoked %d times, want exactly 1: %+v", len(calls), calls)
	}
	return calls[0]
}

// argvEquals asserts the argument vector act received, in order.
func argvEquals(t *testing.T, call testrig.ActCall, want []string) {
	t.Helper()
	if strings.Join(call.Argv, " ") != strings.Join(want, " ") {
		t.Errorf("act argv = %q, want %q", call.Argv, want)
	}
}

// The happy path: act runs the default event and the named job, from the Project
// Root, with the invocation reported in data and act's own output streamed to
// stderr (stdout is the envelope in machine mode).
func TestCILocalRunsTheJobFromTheProjectRoot(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_LINES=2"},
	})
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "ci_local_invocation.json", res.Stdout)

	data := ciLocalData(t, res.Stdout)
	if data.Event != "pull_request" || data.Job != "foundation" || data.Exit != 0 {
		t.Errorf("ci-local reported %+v, want the pull_request event, job foundation, exit 0", data)
	}
	if data.Offline {
		t.Error("ci-local ran offline without --offline or IGDEV_ACT_OFFLINE")
	}
	if data.Workdir != dir {
		t.Errorf("ci-local reported workdir %q, want the Project Root %q", data.Workdir, dir)
	}
	if len(data.OutputTail) != 2 || !strings.HasPrefix(data.OutputTail[0], "act shim line 1") {
		t.Errorf("ci-local reported output_tail %q, want act's two streamed lines", data.OutputTail)
	}

	call := onlyCall(t, env)
	if call.CWD != dir {
		t.Errorf("act ran in %q, want the Project Root %q", call.CWD, dir)
	}
	argvEquals(t, call, []string{"pull_request", "-j", "foundation"})
	// In machine mode a child's own output is progress: on stderr, never mixed
	// into the envelope.
	if !strings.Contains(res.Stderr, "act shim line 1") {
		t.Errorf("act's output is missing from stderr:\n%s", res.Stderr)
	}
	testrig.Envelope(t, res.Stdout)
	res.AssertNoLeaksOutside(t, env.Path("state"))
	env.AssertNoDockerCalls(t)
	env.AssertTempDirEmpty(t)
}

// Human mode streams act's own streams through untouched, and igdev's own lines
// stay on stderr so stdout is exactly what act printed.
func TestCILocalStreamsActOutputForHumans(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_OUT=workflow step ran", "IGDEV_SHIM_ACT_ERR=workflow warning"},
	})
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "ci_local_human_stdout.txt", res.Stdout)
	env.Golden(t, "ci_local_human_stderr.txt", res.Stderr)
	if !strings.Contains(res.Stdout, "workflow step ran") {
		t.Errorf("act's stdout did not reach stdout:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "workflow warning") {
		t.Errorf("act's stderr did not reach stderr:\n%s", res.Stderr)
	}
	res.AssertNoLeaksOutside(t, env.Path("state"))
}

// A non-zero act run is reported as act's own exit level, with the invocation and
// the tail of its output in the failure envelope, so a failing workflow is
// machine-distinguishable from a failure of igdev.
func TestCILocalPropagatesActsExitCode(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_EXIT=7", "IGDEV_SHIM_ACT_OUT=workflow failed"},
	})
	testrig.WantExit(t, res, contract.Exit(7))
	env.Golden(t, "ci_local_act_failed.json", res.Stdout)

	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeActFailed)
	data := ciLocalData(t, res.Stdout)
	if data.Exit != 7 {
		t.Errorf("ci-local reported exit %d, want act's 7", data.Exit)
	}
	if data.Command != "act pull_request -j foundation" {
		t.Errorf("ci-local reported command %q, want the invocation", data.Command)
	}
	if len(data.OutputTail) == 0 || data.OutputTail[0] != "workflow failed" {
		t.Errorf("the failure envelope carries output_tail %q, want act's output", data.OutputTail)
	}
	// The failing run's output still streams: an agent reading stderr sees why.
	if !strings.Contains(res.Stderr, "workflow failed") {
		t.Errorf("act's output is missing from stderr:\n%s", res.Stderr)
	}

	// Human mode reports the same failure as prose and the same exit level.
	human := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_EXIT=7", "IGDEV_SHIM_ACT_ERR=workflow failed"},
	})
	testrig.WantExit(t, human, contract.Exit(7))
	env.Golden(t, "ci_local_act_failed.txt", human.Stderr)
	res.AssertNoLeaksOutside(t, env.Path("state"))
	env.AssertNoDockerCalls(t)
}

// The envelope's tail is bounded: a long act run reports its last lines, not all
// of them.
func TestCILocalReportsTheTailOfALongRun(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_LINES=120"},
	})
	testrig.WantExit(t, res, contract.ExitOK)

	data := ciLocalData(t, res.Stdout)
	if len(data.OutputTail) != 50 {
		t.Fatalf("output_tail has %d lines, want the last 50", len(data.OutputTail))
	}
	if data.OutputTail[0] != "act shim line 71" || data.OutputTail[49] != "act shim line 120" {
		t.Errorf("output_tail spans %q..%q, want the last 50 lines of 120",
			data.OutputTail[0], data.OutputTail[49])
	}
	// Human mode streams everything, tail or no tail.
	human := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_ACT_LINES=120"},
	})
	testrig.WantExit(t, human, contract.ExitOK)
	if !strings.Contains(human.Stdout, "act shim line 1\n") {
		t.Error("the first line of a long run did not stream to a human")
	}
	res.AssertNoLeaksOutside(t, env.Path("state"))
}

// Offline is a resolved setting: the flag and the environment both produce act's
// offline flags, and the flag wins when the two disagree.
func TestCILocalOfflineComesFromTheFlagOrTheEnvironment(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)
	offline := []string{"pull_request", "-j", "foundation", "--pull=false", "--action-offline-mode"}

	flagRun := env.RunIn(dir, "ci-local", "--job", "foundation", "--offline", "--json")
	testrig.WantExit(t, flagRun, contract.ExitOK)
	env.Golden(t, "ci_local_offline.json", flagRun.Stdout)
	if data := ciLocalData(t, flagRun.Stdout); !data.Offline {
		t.Errorf("--offline reported %+v, want offline", data)
	}
	argvEquals(t, onlyCall(t, env), offline)

	// The env tier alone is enough: IGDEV_ACT_OFFLINE=1 is the legacy knob, and it
	// reaches the run through the same five-tier resolver as every other setting.
	envRun := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_ACT_OFFLINE=1"},
	})
	testrig.WantExit(t, envRun, contract.ExitOK)
	if data := ciLocalData(t, envRun.Stdout); !data.Offline {
		t.Errorf("IGDEV_ACT_OFFLINE=1 reported %+v, want offline", data)
	}
	argvEquals(t, env.ActCalls(t)[1], offline)

	// The flag tier wins: an explicit --offline=false outranks the environment.
	conflict := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--offline=false", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_ACT_OFFLINE=1"},
	})
	testrig.WantExit(t, conflict, contract.ExitOK)
	if data := ciLocalData(t, conflict.Stdout); data.Offline {
		t.Errorf("--offline=false reported %+v, want the flag tier to win", data)
	}
	argvEquals(t, env.ActCalls(t)[2], []string{"pull_request", "-j", "foundation"})

	// An explicit "0" in the environment is not offline either.
	off := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_ACT_OFFLINE=0"},
	})
	testrig.WantExit(t, off, contract.ExitOK)
	if data := ciLocalData(t, off.Stdout); data.Offline {
		t.Errorf("IGDEV_ACT_OFFLINE=0 reported %+v, want online", data)
	}
	argvEquals(t, env.ActCalls(t)[3], []string{"pull_request", "-j", "foundation"})
	if got := env.ActCalls(t)[1].Env["IGDEV_ACT_OFFLINE"]; got != "1" {
		t.Errorf("act inherited IGDEV_ACT_OFFLINE=%q, want the environment it was run under", got)
	}
	if len(env.ActCalls(t)) != 4 {
		t.Errorf("act was invoked %d times, want one per run", len(env.ActCalls(t)))
	}
}

// Everything after the known flags reaches act verbatim and in order, including
// flags igdev knows nothing about.
func TestCILocalPassesRemainingArgumentsVerbatim(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.RunIn(dir, "ci-local", "--event", "push", "--job", "foundation", "--offline",
		"--", "--reuse", "--container-architecture", "linux/amd64", "-v", "extra")
	testrig.WantExit(t, res, contract.ExitOK)

	call := onlyCall(t, env)
	argvEquals(t, call, []string{"push", "-j", "foundation", "--pull=false", "--action-offline-mode",
		"--reuse", "--container-architecture", "linux/amd64", "-v", "extra"})
}

// --job is required: leaving it out is a usage error naming the flag, checked
// before anything else runs.
func TestCILocalNeedsAJob(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.RunIn(dir, "ci-local", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "ci_local_missing_job.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeMissingArgument)
	if !strings.Contains(envelope.Message, "--job") {
		t.Errorf("the missing-argument message does not name the flag: %q", envelope.Message)
	}
	testrig.WantRemediation(t, envelope, "igdev ci-local --job foundation")

	// An empty job is the same misuse, and an absent one in a directory with no
	// Project Root is still the usage error: the invocation is wrong before the
	// checkout is.
	empty := env.RunIn(dir, "ci-local", "--job", "", "--json")
	testrig.WantExit(t, empty, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, empty.Stdout), contract.CodeMissingArgument)

	outside := env.Run(testrig.Run{Args: []string{"ci-local", "--json"}, Dir: env.Home})
	testrig.WantExit(t, outside, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, outside.Stdout), contract.CodeMissingArgument)

	if calls := env.ActCalls(t); len(calls) != 0 {
		t.Errorf("act was invoked for a usage error: %+v", calls)
	}
}

// A host without act fails with a named fault whose remediation says how to
// install it, and no subprocess is attempted.
func TestCILocalWithoutActIsANamedFault(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)
	// An empty PATH is the only honest way to test "no act": the scratch PATH
	// holds the shim, so the shim's own log is the evidence that nothing ran.
	empty := env.Mkdir("empty-path")

	res := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation", "--json"},
		Dir:  dir,
		Env:  []string{"PATH=" + empty},
	})
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "ci_local_act_missing.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeActMissing)
	if !strings.Contains(envelope.Message, "act is not on PATH") {
		t.Errorf("the message does not name the missing tool: %q", envelope.Message)
	}
	testrig.WantRemediation(t, envelope, "pipx install act")
	testrig.WantRemediation(t, envelope, "brew install act")
	if calls := env.ActCalls(t); len(calls) != 0 {
		t.Errorf("act was invoked without being on PATH: %+v", calls)
	}

	// Human mode names the code and the fix on stderr.
	human := env.Run(testrig.Run{
		Args: []string{"ci-local", "--job", "foundation"},
		Dir:  dir,
		Env:  []string{"PATH=" + empty},
	})
	testrig.WantExit(t, human, contract.ExitFailure)
	if !strings.Contains(human.Stderr, string(contract.CodeActMissing)) ||
		!strings.Contains(human.Stderr, "pipx install act") {
		t.Errorf("the human report does not name the code and the fix:\n%s", human.Stderr)
	}
	res.AssertNoLeaks(t)
}

// ci-local is a project command: it needs a current Checkout Setup, but not
// Consent — it starts no Gateway and accepts no license.
func TestCILocalNeedsACurrentSetupButNotConsent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()

	// No Project Root at all: there is no repository to run workflows for.
	outside := env.RunIn(env.Home, "ci-local", "--job", "foundation", "--json")
	testrig.WantExit(t, outside, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, outside.Stdout), contract.CodeNotInitialized)

	// A contract with no Checkout Setup: the checkout has not been materialized.
	unsetup := env.Project("unsetup", testrig.MinimalContract)
	notSetup := env.RunIn(unsetup, "ci-local", "--job", "foundation", "--json")
	testrig.WantExit(t, notSetup, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, notSetup.Stdout), contract.CodeSetupRequired)

	// A current Checkout Setup with no Consent recorded is enough: the run is
	// materialized state, not a legal ritual.
	dir := env.Project("repo", testrig.MinimalContract)
	env.SetupStamp(dir, filepath.Join(dir, "igdev.toml"))
	res := env.RunIn(dir, "ci-local", "--job", "foundation", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	argvEquals(t, onlyCall(t, env), []string{"pull_request", "-j", "foundation"})

	// A contract edited after setup is stale: the workflows belong to the
	// materialized checkout.
	env.Write("repo/igdev.toml", testrig.MinimalContract+"\n[ignition]\nversion = \"8.3.8\"\n")
	stale := env.RunIn(dir, "ci-local", "--job", "foundation", "--json")
	testrig.WantExit(t, stale, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, stale.Stdout), contract.CodeSetupStale)

	if calls := env.ActCalls(t); len(calls) != 1 {
		t.Errorf("act was invoked %d times, want only for the materialized checkout", len(calls))
	}
	env.AssertNoDockerCalls(t)
}

// An explicitly empty --event is the default event, not a run with no event.
func TestCILocalEmptyEventIsTheDefaultEvent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)

	res := env.RunIn(dir, "ci-local", "--event", "", "--job", "foundation")
	testrig.WantExit(t, res, contract.ExitOK)
	argvEquals(t, onlyCall(t, env), []string{"pull_request", "-j", "foundation"})
}

// The verb's help is the local reference: what it runs, the three flags, the
// passthrough rule, and what the machine contract reports.
func TestCILocalHelpIsTheReference(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("help", "ci-local")
	testrig.WantExit(t, res, contract.ExitOK)
	for _, want := range []string{"--event", "--job", "--offline", "--json",
		"verbatim", "IGDEV_E_ACT_MISSING", "Project Root", "Examples:"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("ci-local help does not mention %q:\n%s", want, res.Stdout)
		}
	}
	res.AssertNoLeaks(t)
}
