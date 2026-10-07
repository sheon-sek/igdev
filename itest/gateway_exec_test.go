package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// composeTail is the part of a recorded compose call after the frozen
// --project-name/--file/--env-file prefix, which every gateway verb shares.
func composeTail(t *testing.T, env *testrig.Env) string {
	t.Helper()
	calls := env.DockerCalls(t)
	argv := calls[len(calls)-1].Argv
	for i, arg := range argv {
		if arg == "--env-file" && i+2 <= len(argv) {
			return strings.Join(argv[i+2:], " ")
		}
	}
	t.Fatalf("the last docker call carries no --env-file: %v", argv)
	return ""
}

// exec reaches the Instance's Gateway through its own compose project, as the
// Gateway's user unless root is asked for, and passes the command's exit code
// through.
func TestGatewayExecRunsInTheGatewayContainer(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)

	// Not running yet: refused with the verb that fixes it.
	refused := env.RunIn(dir, "gateway", "exec", "--json", "--", "ls")
	testrig.WantExit(t, refused, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, refused.Stdout), contract.CodeGatewayUnhealthy)
	testrig.WantRemediation(t, testrig.Envelope(t, refused.Stdout), "igdev gateway up")

	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	human := env.RunIn(dir, "gateway", "exec", "--", "ls", "-la", "/usr/local/bin/ignition/data")
	testrig.WantExit(t, human, contract.ExitOK)
	if got := composeTail(t, env); got != "exec -T --user ignition gateway ls -la /usr/local/bin/ignition/data" {
		t.Errorf("exec argv tail = %q", got)
	}
	if !strings.Contains(human.Stdout, "gateway ls -la /usr/local/bin/ignition/data") {
		t.Errorf("exec did not stream the command's output:\n%s", human.Stdout)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "exec", "--user", "root", "--workdir", "/tmp", "--", "id"), contract.ExitOK)
	if got := composeTail(t, env); got != "exec -T --user root --workdir /tmp gateway id" {
		t.Errorf("root exec argv tail = %q", got)
	}

	machine := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "exec", "--json", "--", "false"}, Env: []string{"IGDEV_SHIM_EXEC_EXIT=7"}})
	testrig.WantExit(t, machine, contract.Exit(7))
	testrig.WantCode(t, testrig.Envelope(t, machine.Stdout), contract.CodeExecFailed)
	var data struct {
		ExitCode int      `json:"exit_code"`
		Stdout   string   `json:"stdout"`
		Command  []string `json:"command"`
		User     string   `json:"user"`
	}
	testrig.DataOf(t, machine.Stdout, &data)
	if data.ExitCode != 7 || data.User != "ignition" || strings.Join(data.Command, " ") != "false" || !strings.Contains(data.Stdout, "gateway false") {
		t.Errorf("exec --json data = %+v", data)
	}

	bad := env.RunIn(dir, "gateway", "exec", "--user", "admin", "--", "id")
	testrig.WantExit(t, bad, contract.ExitUsage)
	none := env.RunIn(dir, "gateway", "exec")
	testrig.WantExit(t, none, contract.ExitUsage)

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// data put and get address files relative to the Gateway's data directory, as
// the Gateway's own user, and refuse any path that could leave it before the
// engine is asked anything.
func TestGatewayDataMovesFilesUnderTheDataDirectory(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	marker := filepath.Join(dir, "run.once")
	if err := os.WriteFile(marker, []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	put := env.RunIn(dir, "gateway", "data", "put", "run.once", "engineering-tools/proof/run-udt-sdk-proof.once", "--json")
	testrig.WantExit(t, put, contract.ExitOK)
	tail := composeTail(t, env)
	if !strings.HasPrefix(tail, "exec -T --user 2003:0 gateway sh -c ") ||
		!strings.HasSuffix(tail, "igdev-data-put /usr/local/bin/ignition/data/engineering-tools/proof/run-udt-sdk-proof.once") {
		t.Errorf("put argv tail = %q", tail)
	}
	var putData struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	testrig.DataOf(t, put.Stdout, &putData)
	if putData.Bytes != 3 || putData.SHA256 != "c2fc355f2b52e01ea670dc8b27f1c8f3a268d68b4b399a0cf91544cb975792df" || putData.Path != "engineering-tools/proof/run-udt-sdk-proof.once" {
		t.Errorf("put data = %+v", putData)
	}

	get := env.RunIn(dir, "gateway", "data", "get", "engineering-tools/proof/udt-sdk-proof-latest.json", "report.json")
	testrig.WantExit(t, get, contract.ExitOK)
	body, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil || string(body) != "shim data file\n" {
		t.Errorf("get wrote %q (err %v)", body, err)
	}
	inline := env.RunIn(dir, "gateway", "data", "get", "engineering-tools/proof/udt-sdk-proof-latest.json", "--json")
	testrig.WantExit(t, inline, contract.ExitOK)
	if !strings.Contains(inline.Stdout, `"content_base64": "c2hpbSBkYXRhIGZpbGUK"`) {
		t.Errorf("get --json does not carry the file:\n%s", inline.Stdout)
	}

	// A failed get leaves nothing at the local path.
	missing := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "data", "get", "nope.json", "nope.json", "--json"}, Env: []string{"IGDEV_SHIM_EXEC_EXIT=98"}})
	testrig.WantExit(t, missing, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, missing.Stdout), contract.CodeExecFailed)
	if _, err := os.Stat(filepath.Join(dir, "nope.json")); !os.IsNotExist(err) {
		t.Errorf("a failed get left a file behind (err %v)", err)
	}

	before := len(env.DockerCalls(t))
	for _, path := range []string{"../etc/passwd", "a/../../b", "/etc/passwd", "", "."} {
		res := env.RunIn(dir, "gateway", "data", "get", path, "--json")
		testrig.WantExit(t, res, contract.ExitUsage)
		testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
	}
	if after := len(env.DockerCalls(t)); after != before {
		t.Errorf("a refused path still reached the engine (%d calls)", after-before)
	}
	// A symlink escape is caught in the container, and is a usage error too.
	escape := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "data", "get", "link/passwd", "--json"}, Env: []string{"IGDEV_SHIM_EXEC_EXIT=97"}})
	testrig.WantExit(t, escape, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, escape.Stdout), contract.CodeUsage)

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}
