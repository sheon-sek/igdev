package itest

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// --with-gateway ensures the Instance's Gateway, puts the job on its compose
// network, and hands it the URL and the token through files, never argv; the
// files are gone after the run.
func TestCILocalWithGatewayWiresTheJob(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/data/api/v1/gateway-info": 200})

	res := env.Run(testrig.Run{Dir: dir, Args: []string{"ci-local", "--job", "e2e", "--with-gateway", "--json"}})
	testrig.WantExit(t, res, contract.ExitOK)

	call := onlyCall(t, env)
	argv := call.Argv
	at := func(flag string) string {
		i := slices.Index(argv, flag)
		if i < 0 || i+1 >= len(argv) {
			t.Fatalf("act argv carries no %s: %v", flag, argv)
		}
		return argv[i+1]
	}
	if got, want := at("--network"), stamp.Namespace()+"_default"; got != want {
		t.Errorf("--network = %q, want %q", got, want)
	}
	files := filepath.Dir(at("--env-file"))
	if filepath.Dir(at("--secret-file")) != files {
		t.Errorf("the env and secret files are not side by side: %v", argv)
	}
	if call.EnvFileKeys != "IGDEV_GATEWAY_URL,IGDEV_GATEWAY_TOKEN" || call.SecretFileKeys != "IGDEV_GATEWAY_TOKEN" {
		t.Errorf("env file keys %q, secret file keys %q", call.EnvFileKeys, call.SecretFileKeys)
	}
	token := gatewayToken(t, env, dir)
	if token == "" || strings.Contains(strings.Join(argv, " "), token) || strings.Contains(res.Stdout+res.Stderr, token) {
		t.Error("the Instance token is in act's argv or igdev's output")
	}
	env.RegisterReplacement(files, "<ACT_FILES>")
	env.Golden(t, "ci_local_with_gateway.json", res.Stdout)
	env.AssertTempDirEmpty(t)

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
}

// Without the flag nothing changes: no engine call, no extra act argument.
func TestCILocalWithoutTheFlagTouchesNoGateway(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimAct()
	dir := ciLocalFixture(t, env)
	testrig.WantExit(t, env.RunIn(dir, "ci-local", "--job", "foundation"), contract.ExitOK)
	argvEquals(t, onlyCall(t, env), []string{"pull_request", "-j", "foundation"})
	env.AssertNoDockerCalls(t)
}

func gatewayToken(t *testing.T, env *testrig.Env, dir string) string {
	t.Helper()
	var creds struct {
		APIToken string `json:"api_token"`
	}
	res := env.RunIn(dir, "gateway", "credentials", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	testrig.DataOf(t, res.Stdout, &creds)
	return creds.APIToken
}
