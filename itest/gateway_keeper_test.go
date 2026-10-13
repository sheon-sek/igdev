package itest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// shimContainer is where the docker shim keeps one container of the engine it
// stands in for: deleting the file is that container stopping and going away
// behind igdev's back, as a manual `docker stop`/`docker rm` would.
func shimContainer(env *testrig.Env, name string) string {
	return filepath.Join(env.Path("state"), "docker", "containers", name)
}

func shimHas(env *testrig.Env, name string) bool {
	_, err := os.Stat(shimContainer(env, name))
	return err == nil
}

func removeShimContainer(t *testing.T, env *testrig.Env, name string) {
	t.Helper()
	if err := os.Remove(shimContainer(env, name)); err != nil {
		t.Fatalf("remove the shim's %s: %v", name, err)
	}
}

// switchContract rewrites igdev.toml and re-runs setup, keeping the pinned ports
// so the stand-in Gateway stays reachable.
func switchContract(t *testing.T, env *testrig.Env, dir string, stamp gate.Stamp, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, project.ContractFile), []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite the contract: %v", err)
	}
	testrig.WantExit(t, env.RunIn(dir, "setup"), contract.ExitOK)
	pinPorts(t, env, dir, stamp.Ports)
}

// callsSince is the engine calls recorded after the first n, as joined argv.
func callsSince(t *testing.T, env *testrig.Env, n int) []string {
	t.Helper()
	out := []string{}
	for _, call := range env.DockerCalls(t)[n:] {
		argv := call.Argv
		if len(argv) > 0 && filepath.Base(argv[0]) == "docker" {
			argv = argv[1:]
		}
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// wantNoGatewayTouch fails when a call would build, restart, reset, or otherwise
// recreate the Gateway: the reuse path converges the keeper only.
func wantNoGatewayTouch(t *testing.T, calls []string) {
	t.Helper()
	for _, call := range calls {
		fields := strings.Fields(call)
		if len(fields) == 0 || fields[0] != "compose" {
			continue
		}
		verb := testrig.ComposeVerb(fields[1:])
		if verb == "restart" || verb == "down" ||
			(verb == "up" && (slices.Contains(fields, "--build") || slices.Contains(fields, "gateway") ||
				!slices.Contains(fields, "--no-deps"))) {
			t.Errorf("the reuse path touched the Gateway: %q", call)
		}
	}
}

// The Gateway's state is the gateway service's own: a trial keeper that still
// runs next to a stopped or removed Gateway does not make status report running,
// exec does not treat the Gateway as up, and ensure starts it instead of waiting
// on it (igdev#147).
func TestGatewayStateIsTheGatewayServiceAlone(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})
	ns := stamp.Namespace()

	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	var status struct {
		State    string `json:"state"`
		Services []struct {
			Service string `json:"service"`
			State   string `json:"state"`
		} `json:"services"`
	}
	res := env.RunIn(dir, "gateway", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	testrig.DataOf(t, res.Stdout, &status)
	if status.State != "running" || len(status.Services) != 2 {
		t.Fatalf("status with Gateway and keeper = %+v, want running with both services", status)
	}

	removeShimContainer(t, env, testrig.ComposeContainer(ns))
	res = env.RunIn(dir, "gateway", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	testrig.DataOf(t, res.Stdout, &status)
	if status.State == "running" {
		t.Errorf("status with only the keeper running = %q, want the Gateway's own state", status.State)
	}
	if len(status.Services) != 1 || status.Services[0].Service != "trial-keeper" {
		t.Errorf("status services = %+v, want the keeper still listed", status.Services)
	}

	refused := env.RunIn(dir, "gateway", "exec", "--json", "--", "ls")
	testrig.WantExit(t, refused, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, refused.Stdout), contract.CodeGatewayUnhealthy)

	before := len(env.DockerCalls(t))
	if _, data := runEnsure(t, env, dir); data.Action != "started" || data.Reason != "not_running" {
		t.Errorf("ensure with only the keeper running = %s/%s, want started/not_running", data.Action, data.Reason)
	}
	if calls := callsSince(t, env, before); !slices.ContainsFunc(calls, func(c string) bool {
		return strings.HasSuffix(c, " up --detach --build gateway trial-keeper")
	}) {
		t.Errorf("ensure did not start the Gateway: %v", calls)
	}
	if !shimHas(env, testrig.ComposeContainer(ns)) {
		t.Error("ensure left the Gateway absent")
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// ensure on a healthy Gateway converges the keeper on the contract — starts a
// missing one, drops one the contract switched off, starts it again when it is
// switched back on — without building, restarting, or resetting the Gateway, and
// a repeated ensure changes nothing (igdev#147).
func TestGatewayEnsureReconcilesTheTrialKeeper(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})
	ns := stamp.Namespace()
	gateway, keeper := testrig.ComposeContainer(ns), testrig.ComposeKeeperContainer(ns)
	volume := filepath.Join(env.Path("state"), "docker", "volumes", testrig.ComposeVolume(ns))

	if _, data := runEnsure(t, env, dir); data.Action != "started" {
		t.Fatalf("first ensure = %s, want started", data.Action)
	}
	if !shimHas(env, keeper) {
		t.Fatal("ensure did not start the keeper the contract asks for")
	}

	// A keeper that went away next to a healthy Gateway is started alone.
	removeShimContainer(t, env, keeper)
	before := len(env.DockerCalls(t))
	res, data := runEnsure(t, env, dir)
	if data.Action != "reused" || data.Reason != "healthy" {
		t.Errorf("ensure with a missing keeper = %s/%s, want reused/healthy", data.Action, data.Reason)
	}
	calls := callsSince(t, env, before)
	if !slices.ContainsFunc(calls, func(c string) bool {
		return strings.HasSuffix(c, " up --detach --no-deps --no-build trial-keeper")
	}) {
		t.Errorf("ensure did not start the missing keeper alone: %v", calls)
	}
	wantNoGatewayTouch(t, calls)
	if !shimHas(env, keeper) || !shimHas(env, gateway) {
		t.Error("the keeper was not started, or the Gateway went away")
	}
	if !strings.Contains(res.Stderr, "starting the trial keeper") {
		t.Errorf("ensure did not say it started the keeper:\n%s", res.Stderr)
	}

	// Converged: a repeated ensure only reads the engine.
	before = len(env.DockerCalls(t))
	runEnsure(t, env, dir)
	if got := gatewayVerbs(t, env)[before:]; !slices.Equal(got, []string{ns + ":ps"}) {
		t.Errorf("a converged ensure called %v, want only ps", got)
	}

	// auto → off: the keeper the contract no longer runs is removed, the Gateway
	// and its volume stay.
	switchContract(t, env, dir, stamp, trialOff)
	before = len(env.DockerCalls(t))
	res, data = runEnsure(t, env, dir)
	if data.Action != "reused" || data.Reason != "healthy" {
		t.Errorf("ensure after trial_reset off = %s/%s, want reused/healthy", data.Action, data.Reason)
	}
	calls = callsSince(t, env, before)
	if !slices.Contains(calls, "rm --force "+keeper) {
		t.Errorf("ensure did not remove the obsolete keeper by name: %v", calls)
	}
	wantNoGatewayTouch(t, calls)
	if shimHas(env, keeper) {
		t.Error("the obsolete keeper survived ensure")
	}
	if !shimHas(env, gateway) {
		t.Error("removing the keeper took the Gateway with it")
	}
	if _, err := os.Stat(volume); err != nil {
		t.Errorf("removing the keeper touched the Gateway's volume: %v", err)
	}
	if !strings.Contains(res.Stderr, "removed the trial keeper the contract no longer runs") {
		t.Errorf("ensure did not say it removed the keeper:\n%s", res.Stderr)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "container ls") &&
			(!strings.Contains(call, "label=com.docker.compose.project="+ns) ||
				!strings.Contains(call, "label=com.docker.compose.service=trial-keeper") ||
				!strings.Contains(call, "label=com.docker.compose.oneoff=False")) {
			t.Errorf("the keeper lookup is not scoped to this project's Compose keeper: %q", call)
		}
	}

	// Converged under off: nothing left to remove.
	before = len(env.DockerCalls(t))
	runEnsure(t, env, dir)
	for _, call := range callsSince(t, env, before) {
		if strings.HasPrefix(call, "rm ") {
			t.Errorf("a converged ensure removed something: %q", call)
		}
	}

	// off → auto: the keeper is started next to the reused Gateway.
	switchContract(t, env, dir, stamp, testrig.MinimalContract)
	before = len(env.DockerCalls(t))
	if _, data = runEnsure(t, env, dir); data.Action != "reused" {
		t.Errorf("ensure after trial_reset auto = %s, want reused", data.Action)
	}
	calls = callsSince(t, env, before)
	wantNoGatewayTouch(t, calls)
	if !shimHas(env, keeper) {
		t.Errorf("ensure did not start the keeper the contract asks for again: %v", calls)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// up under a contract without the keeper removes the one an earlier contract
// started: a plain `up` of the Gateway would leave it resetting a trial the
// contract no longer resets (igdev#147).
func TestGatewayUpRemovesAnObsoleteTrialKeeper(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	ns := stamp.Namespace()
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	switchContract(t, env, dir, stamp, trialOff)
	before := len(env.DockerCalls(t))
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	calls := callsSince(t, env, before)
	if shimHas(env, testrig.ComposeKeeperContainer(ns)) {
		t.Errorf("up left the obsolete keeper running: %v", calls)
	}
	if !shimHas(env, testrig.ComposeContainer(ns)) {
		t.Error("up did not leave the Gateway running")
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// A keeper that cannot be started or removed is a failure: ensure never reports
// a converged, healthy reuse it did not reach (igdev#147).
func TestGatewayEnsureReportsAKeeperFailure(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})
	ns := stamp.Namespace()
	runEnsure(t, env, dir)

	ensureFails := func(what string) {
		t.Helper()
		res := env.Run(testrig.Run{Dir: dir, Env: []string{"IGDEV_SHIM_KEEPER_FAIL=1"},
			Args: []string{"gateway", "ensure", "--timeout", "5", "--json"}})
		testrig.WantExit(t, res, contract.ExitFailure)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeDocker)
		if envelope.Ok {
			t.Errorf("a failed %s reported ok", what)
		}
	}

	removeShimContainer(t, env, testrig.ComposeKeeperContainer(ns))
	ensureFails("keeper start")

	runEnsure(t, env, dir)
	switchContract(t, env, dir, stamp, trialOff)
	ensureFails("keeper removal")
	if !shimHas(env, testrig.ComposeKeeperContainer(ns)) {
		t.Error("the shim removed the keeper it was told to fail on")
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
}
