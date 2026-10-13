package itest

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The interrupt tests run the real binary and send it real signals: a signal to
// its process group is a terminal's Ctrl-C, which act and docker receive too; a
// signal to igdev alone is a supervisor's SIGTERM, which its children never see.
// Either way igdev removes what it created, then dies by that same signal, so a
// shell sees the status it always saw and stdout carries no envelope.

const interruptWait = 20 * time.Second

// tempEntries lists TMPDIR entries whose name starts with prefix.
func tempEntries(t *testing.T, env *testrig.Env, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(env.Temp)
	if err != nil {
		t.Fatalf("read TMPDIR: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			out = append(out, entry.Name())
		}
	}
	return out
}

// waitTempGone polls until TMPDIR holds nothing starting with prefix.
func waitTempGone(t *testing.T, env *testrig.Env, prefix string) {
	t.Helper()
	deadline := time.Now().Add(interruptWait)
	for len(tempEntries(t, env, prefix)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%s* is still in TMPDIR: %v", prefix, tempEntries(t, env, prefix))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wantDiedBy asserts igdev ended by sig after saying so, with nothing on stdout.
func wantDiedBy(t *testing.T, res testrig.Result, sig syscall.Signal, name string) {
	t.Helper()
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Signal != sig {
		t.Fatalf("igdev ended with exit %d signal %v, want death by %v\nstdout: %s\nstderr: %s",
			res.Exit, res.Signal, sig, res.Stdout, res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("an interrupted run printed on stdout: %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "[igdev] interrupted by "+name+": cleaning up before exit") {
		t.Errorf("stderr does not report the interrupt: %q", res.Stderr)
	}
}

// startCILocal starts `ci-local --with-gateway` with act holding, and returns
// once act is running with the token files written.
func startCILocal(t *testing.T, hold string) (*testrig.Env, string, *testrig.Proc) {
	t.Helper()
	env := testrig.NewEnv(t)
	env.ShimAct()
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})

	proc := env.Start(testrig.Run{Dir: dir, Args: []string{"ci-local", "--job", "e2e", "--with-gateway", "--json"},
		Env: []string{"IGDEV_SHIM_ACT_HOLD=" + hold}})
	testrig.WaitForFile(t, env.Path("state", "act-holding"), interruptWait)
	if got := tempEntries(t, env, "igdev-ci-local-"); len(got) != 1 {
		t.Fatalf("igdev-ci-local-* while act runs = %v, want one directory", got)
	}
	return env, dir, proc
}

// wantNoToken asserts the Instance token reached neither stream.
func wantNoToken(t *testing.T, env *testrig.Env, dir string, res testrig.Result) {
	t.Helper()
	token := gatewayToken(t, env, dir)
	if token == "" || strings.Contains(res.Stdout+res.Stderr, token) {
		t.Error("the Instance token is in igdev's output")
	}
}

// Ctrl-C during `ci-local --with-gateway`: act stops, and the directory that
// carries the token files is removed before igdev dies by SIGINT.
func TestInterruptCILocalCtrlCRemovesTheTokenFiles(t *testing.T) {
	env, dir, proc := startCILocal(t, "30")
	proc.SignalGroup(syscall.SIGINT)
	res := proc.Wait(interruptWait)

	wantDiedBy(t, res, syscall.SIGINT, "SIGINT")
	if got := tempEntries(t, env, "igdev-ci-local-"); len(got) != 0 {
		t.Errorf("Ctrl-C left %v in TMPDIR", got)
	}
	env.AssertTempDirEmpty(t)
	wantNoToken(t, env, dir, res)
}

// A SIGTERM to igdev alone never reaches act. igdev removes the token files at
// once — while act is still running — then waits for act, and dies by SIGTERM.
func TestInterruptCILocalSIGTERMRemovesTheTokenFilesAndWaitsForAct(t *testing.T) {
	env, dir, proc := startCILocal(t, "3")
	proc.Signal(syscall.SIGTERM)
	waitTempGone(t, env, "igdev-ci-local-")
	if _, err := os.Stat(env.Path("state", "act-held")); err == nil {
		t.Error("the token files outlived act: cleanup must not wait for the child")
	}
	res := proc.Wait(interruptWait)

	wantDiedBy(t, res, syscall.SIGTERM, "SIGTERM")
	if _, err := os.Stat(env.Path("state", "act-held")); err != nil {
		t.Errorf("igdev exited before act did: %v", err)
	}
	if !strings.Contains(res.Stderr, "[igdev] waiting for the running child process to exit") {
		t.Errorf("stderr does not say igdev waits for act: %q", res.Stderr)
	}
	env.AssertTempDirEmpty(t)
	wantNoToken(t, env, dir, res)
}

// A second signal stops the wait for a child that does not exit; the token
// files are already gone by then.
func TestInterruptSecondSignalStopsTheWait(t *testing.T) {
	env, _, proc := startCILocal(t, "60")
	proc.Signal(syscall.SIGTERM)
	waitTempGone(t, env, "igdev-ci-local-")
	proc.Signal(syscall.SIGTERM)
	res := proc.Wait(interruptWait)

	wantDiedBy(t, res, syscall.SIGTERM, "SIGTERM")
	if !strings.Contains(res.Stderr, "[igdev] interrupted again by SIGTERM: exiting without waiting") {
		t.Errorf("stderr does not report the second signal: %q", res.Stderr)
	}
	env.AssertTempDirEmpty(t)
}

// startLookup starts a lookup index build with `docker cp` holding, and returns
// once the copy is running inside the scratch-backed build.
func startLookup(t *testing.T, hold string) (*testrig.Env, *testrig.Proc) {
	t.Helper()
	env := testrig.NewEnv(t)
	env.ShimDocker()
	lookupImage(t, env)
	dir := env.Mkdir("elsewhere")

	proc := env.Start(testrig.Run{Dir: dir, Args: []string{"lookup", "read tag values", "--json"},
		Env: []string{"IGDEV_SHIM_DOCKER_CP_HOLD=" + hold}})
	testrig.WaitForFile(t, env.Path("state", "docker-cp-holding"), interruptWait)
	if got := tempEntries(t, env, "igdev-lookup-"); len(got) != 1 {
		t.Fatalf("igdev-lookup-* while the index builds = %v, want one directory", got)
	}
	return env, proc
}

// wantContainerRemoved asserts the container lookup created was removed.
func wantContainerRemoved(t *testing.T, env *testrig.Env) {
	t.Helper()
	id := ""
	removed := false
	for _, call := range env.DockerCalls(t) {
		argv := strings.Join(call.Argv, " ")
		if strings.HasPrefix(argv, "create ") {
			id = testrig.SyntheticContainerID(call.N)
		}
		if id != "" && argv == "rm --force "+id {
			removed = true
		}
	}
	if id == "" || !removed {
		t.Errorf("the lookup container %q was not removed: %+v", id, env.DockerCalls(t))
	}
}

// Ctrl-C during a lookup index build removes the scratch directory and the
// temporary container.
func TestInterruptLookupCtrlCRemovesScratchAndContainer(t *testing.T) {
	env, proc := startLookup(t, "30")
	proc.SignalGroup(syscall.SIGINT)
	res := proc.Wait(interruptWait)

	wantDiedBy(t, res, syscall.SIGINT, "SIGINT")
	env.AssertTempDirEmpty(t)
	wantContainerRemoved(t, env)
}

// A SIGTERM to igdev alone during a lookup index build does the same, after the
// copy it was waiting on finishes.
func TestInterruptLookupSIGTERMRemovesScratchAndContainer(t *testing.T) {
	env, proc := startLookup(t, "2")
	proc.Signal(syscall.SIGTERM)
	res := proc.Wait(interruptWait)

	wantDiedBy(t, res, syscall.SIGTERM, "SIGTERM")
	env.AssertTempDirEmpty(t)
	wantContainerRemoved(t, env)
}

// A run nobody interrupts is unchanged by the handler: it exits on its own,
// with its envelope, and says nothing about interrupts.
func TestInterruptNormalExitIsUnchanged(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	lookupImage(t, env)
	res := env.Run(testrig.Run{Dir: env.Mkdir("elsewhere"), Args: []string{"lookup", "read tag values", "--json"},
		Env: []string{"IGDEV_SHIM_DOCKER_CP_HOLD=0"}})
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Signal != 0 || strings.Contains(res.Stderr, "interrupted") {
		t.Errorf("an uninterrupted run reported an interrupt: signal %v, stderr %q", res.Signal, res.Stderr)
	}
	env.AssertTempDirEmpty(t)
	if _, err := os.Stat(filepath.Join(env.Root, "state", "docker-cp-held")); err != nil {
		t.Errorf("the copy did not run: %v", err)
	}
}
