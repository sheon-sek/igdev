package itest

import (
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// daemonDownOutput is what the docker CLI prints when its daemon is stopped.
const daemonDownOutput = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"

// A stopped daemon is its own code, so a caller starts Docker instead of
// resetting the Gateway; an ordinary compose failure keeps the generic code.
func TestGatewayReportsAStoppedDaemonApart(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)

	down := []string{"IGDEV_SHIM_DOCKER_OUT=" + daemonDownOutput, "IGDEV_SHIM_DOCKER_EXIT=1"}
	for _, verb := range [][]string{{"gateway", "up", "--force", "--json"}, {"gateway", "ensure", "--force", "--json"}, {"gateway", "status", "--json"}} {
		res := env.Run(testrig.Run{Dir: dir, Args: verb, Env: down})
		testrig.WantExit(t, res, contract.ExitFailure)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeDockerDaemon)
		testrig.WantRemediation(t, envelope, "igdev doctor")
	}

	refused := env.Run(testrig.Run{Dir: dir, Args: []string{"gateway", "up", "--force", "--json"},
		Env: []string{"IGDEV_SHIM_DOCKER_OUT=service \"gateway\" refused to start", "IGDEV_SHIM_DOCKER_EXIT=1"}})
	testrig.WantExit(t, refused, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, refused.Stdout), contract.CodeDocker)
}
