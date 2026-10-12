package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// volumeReads counts the guard's reads of a data volume's init record.
func volumeReads(t *testing.T, env *testrig.Env) (inspects, cats int) {
	t.Helper()
	for _, call := range env.AllDockerCalls(t) {
		argv := strings.Join(call.Argv, " ")
		switch {
		case strings.HasPrefix(argv, "volume inspect"):
			inspects++
		case strings.HasPrefix(argv, "run --rm --entrypoint cat"):
			cats++
		}
	}
	return inspects, cats
}

// Starting the Gateway on an existing data volume compares the version that last
// ran the volume with the contract's: equal is silent, older notes the one-way
// upgrade and starts, newer refuses before compose is asked to start anything
// (issue #126). Without a volume nothing beyond the existence check runs.
func TestGatewayVolumeVersionGuard(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)
	want := project.DefaultDoc().Ignition.Version

	first := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, first, contract.ExitOK)
	if inspects, cats := volumeReads(t, env); inspects != 1 || cats != 0 {
		t.Errorf("no volume: %d inspects, %d cats; want 1 and 0", inspects, cats)
	}

	env.SetBaseEnv("IGDEV_SHIM_VOLUME_INIT=volume_version=" + want)
	equal := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, equal, contract.ExitOK)
	if strings.Contains(equal.Stderr, "upgrades it one way") {
		t.Errorf("equal version printed an upgrade note:\n%s", equal.Stderr)
	}

	env.SetBaseEnv("IGDEV_SHIM_VOLUME_INIT=volume_version=1.0.0")
	older := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, older, contract.ExitOK)
	if !strings.Contains(older.Stderr, "created by Ignition 1.0.0") || !strings.Contains(older.Stderr, "upgrades it one way") {
		t.Errorf("older volume: no upgrade note on stderr:\n%s", older.Stderr)
	}

	env.SetBaseEnv("IGDEV_SHIM_VOLUME_INIT=volume_version=99.0.0")
	before := len(env.DockerCalls(t))
	newer := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, newer, contract.ExitFailure)
	out := newer.Stdout
	for _, s := range []string{string(contract.CodeGatewayUnhealthy), "99.0.0", want, "igdev setup", "igdev gateway reset", "deletes the volume's data"} {
		if !strings.Contains(out, s) {
			t.Errorf("newer volume: response lacks %q:\n%s", s, out)
		}
	}
	for _, call := range env.DockerCalls(t)[before:] {
		if strings.Contains(strings.Join(call.Argv, " "), " up ") {
			t.Errorf("newer volume still started compose: %v", call.Argv)
		}
	}
	restart := env.RunIn(dir, "restart", "--json")
	testrig.WantExit(t, restart, contract.ExitFailure)
	if !strings.Contains(restart.Stdout, "99.0.0") {
		t.Errorf("restart did not refuse the newer volume:\n%s", restart.Stdout)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}
