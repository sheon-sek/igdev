package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// An unknown command is a usage error at exit level 2 with IGDEV_E_USAGE, in the
// machine envelope, and it still names a way forward.
func TestUnknownCommandJSON(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()

	res := env.MustRun("stat", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "unknown_command.json", res.Stdout)

	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUsage)
	testrig.WantRemediation(t, envelope, "igdev --help")
	res.AssertNoLeaks(t)
}

// The same failure in human mode goes to stderr and leaves stdout empty, so a
// pipe never receives prose.
func TestUnknownCommandHuman(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("stat")
	testrig.WantExit(t, res, contract.ExitUsage)
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty in human mode", res.Stdout)
	}
	env.Golden(t, "unknown_command.txt", res.Stderr)
}

// An unknown flag is a usage error too, and --json on the command line still
// selects the machine dialect even though the flags never finished parsing.
func TestUnknownFlag(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("status", "--nope", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "unknown_flag.json", res.Stdout)

	res = env.MustRun("status", "--nope")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "unknown_flag.txt", res.Stderr)
}

// A required argument that was not supplied is IGDEV_E_MISSING_ARGUMENT at exit
// 2, naming the argument exactly.
func TestCompletionMissingShell(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("completion", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "missing_argument.json", res.Stdout)

	res = env.MustRun("completion")
	testrig.WantExit(t, res, contract.ExitUsage)
	if !strings.Contains(res.Stderr, "<shell>") {
		t.Errorf("human error does not name the missing argument: %q", res.Stderr)
	}
}

// An unsupported value for a known argument is a usage error naming the
// accepted set.
func TestCompletionUnsupportedShell(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("completion", "nushell", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
	if !strings.Contains(res.Stdout, "bash") {
		t.Errorf("envelope does not list the supported shells:\n%s", res.Stdout)
	}
}

// A command that takes no arguments says so at exit 2 rather than ignoring the
// extras.
func TestStatusRejectsArguments(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("status", "surprise", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
}

// A --config typo is part of the invocation, so it is a usage error at exit 2
// even though a bad value in a file tier is a config failure at exit 1.
func TestUnknownConfigKey(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("status", "--json", "--config", "outpup.format=json")
	testrig.WantExit(t, res, contract.ExitUsage)
	env.Golden(t, "unknown_config_key.json", res.Stdout)
}
