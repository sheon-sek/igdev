package itest

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// trialOff is a contract whose Gateway runs without the trial keeper, so
// --min-trial applies.
const trialOff = testrig.MinimalContract + `
[gateway]
trial_reset = "off"
`

// ensureData is the part of the ensure report the tests branch on.
type ensureData struct {
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	Container string `json:"container"`
	Note      string `json:"note"`
}

// readyAfterDown makes the stand-in Gateway report RUNNING once the engine has
// been asked to discard the volume, standing in for the fresh Gateway a reset
// starts.
func readyAfterDown(env *testrig.Env, stub *testrig.GatewayStub) {
	calls := env.Path("state", "docker-calls.jsonl")
	go func() {
		for range 400 {
			if raw, err := os.ReadFile(calls); err == nil && strings.Contains(string(raw), `"down","--volumes"`) {
				stub.Ready()
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
}

func runEnsure(t *testing.T, env *testrig.Env, dir string, args ...string) (testrig.Result, ensureData) {
	t.Helper()
	res := env.Run(testrig.Run{Dir: dir, Args: append([]string{"gateway", "ensure", "--timeout", "5", "--json"}, args...)})
	testrig.WantExit(t, res, contract.ExitOK)
	var data ensureData
	testrig.DataOf(t, res.Stdout, &data)
	return res, data
}

// ensure starts a Gateway that is not running, reuses a healthy one without
// touching the engine, and resets one that is FAULTED, rejects the token, or is
// asked to start fresh.
func TestGatewayEnsureStartsReusesAndResets(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})

	res, data := runEnsure(t, env, dir)
	env.Golden(t, "gateway_ensure_started.json", res.Stdout)
	if data.Action != "started" || data.Reason != "not_running" {
		t.Errorf("first ensure = %s/%s, want started/not_running", data.Action, data.Reason)
	}

	res, data = runEnsure(t, env, dir)
	env.Golden(t, "gateway_ensure_reused.json", res.Stdout)
	if data.Action != "reused" {
		t.Errorf("ensure on a healthy Gateway = %s, want reused", data.Action)
	}
	ns := stamp.Namespace()
	if got, want := gatewayVerbs(t, env), []string{ns + ":ps", ns + ":up", ns + ":ps"}; !slices.Equal(got, want) {
		t.Errorf("engine calls = %v, want %v", got, want)
	}

	// Under the default trial keeper --min-trial is accepted and reported as not
	// applying, never as a reason to reset.
	if _, data = runEnsure(t, env, dir, "--min-trial", "1000h"); data.Action != "reused" || data.Note == "" {
		t.Errorf("--min-trial under trial_reset auto = %s (note %q), want reused with a note", data.Action, data.Note)
	}

	stub.SetBody("/StatusPing", `{"state":"FAULTED"}`)
	readyAfterDown(env, stub)
	res, data = runEnsure(t, env, dir)
	env.Golden(t, "gateway_ensure_reset.json", res.Stdout)
	if data.Action != "reset" || data.Reason != "faulted" {
		t.Errorf("ensure on a FAULTED Gateway = %s/%s, want reset/faulted", data.Action, data.Reason)
	}

	stub.SetStatus("/data/api/v1/gateway-info", 401)
	if _, data = runEnsure(t, env, dir); data.Reason != "token_rejected" {
		t.Errorf("ensure with a rejected token = %s/%s, want reset/token_rejected", data.Action, data.Reason)
	}
	stub.SetStatus("/data/api/v1/gateway-info", 200)

	if _, data = runEnsure(t, env, dir, "--fresh"); data.Action != "reset" || data.Reason != "fresh" {
		t.Errorf("ensure --fresh = %s/%s, want reset/fresh", data.Action, data.Reason)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// --min-trial resets a Gateway that is short of trial only when nothing resets
// the trial in place; under the default trial keeper the report says it did not
// apply.
func TestGatewayEnsureMinTrial(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, trialOff)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{
		"/data/api/v1/gateway-info": 200, "/data/api/v1/trial": 200,
	})
	stub.SetBody("/data/api/v1/trial", `{"licenseMode":"Trial","trialSecondsLeft":600,"expired":false}`)

	if _, data := runEnsure(t, env, dir, "--min-trial", "5m"); data.Action != "reused" {
		t.Errorf("10m left against --min-trial 5m = %s, want reused", data.Action)
	}
	if _, data := runEnsure(t, env, dir, "--min-trial", "30m"); data.Action != "reset" || data.Reason != "trial_short" {
		t.Errorf("10m left against --min-trial 30m = %s/%s, want reset/trial_short", data.Action, data.Reason)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
}

// Low memory does not stop ensure --fresh: it warns and resets (igdev#105).
func TestGatewayEnsureLowMemoryWarnsAndResets(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	testrig.ServeGateway(t, stamp.Ports.HTTP, nil)

	env.ShimMeminfo(1024)
	res := env.RunIn(dir, "gateway", "ensure", "--fresh", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stderr, "low memory, starting anyway") {
		t.Errorf("ensure did not warn about memory:\n%s", res.Stderr)
	}
	if !slices.Contains(gatewayVerbs(t, env), stamp.Namespace()+":down") {
		t.Errorf("ensure --fresh did not reset: %v", gatewayVerbs(t, env))
	}
	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
}
