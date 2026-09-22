package itest

import (
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// version reports both identities an agent needs: the release semver of the
// binary and the CLI Contract Version it speaks.
func TestVersionJSON(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()

	res := env.MustRun("version", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "version.json", res.Stdout)
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)
}

// Human mode prints the same two values as text.
func TestVersionHuman(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "version.txt", res.Stdout)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty with the notifier off", res.Stderr)
	}
	res.AssertNoLeaks(t)
}

// version never invents an argument.
func TestVersionRejectsArguments(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("version", "extra", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
}
