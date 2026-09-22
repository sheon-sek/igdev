package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// `igdev help` is the complete local reference: it lists the verbs and carries
// the agent contract summary.
func TestHelpListsCommands(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("help")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "help.txt", res.Stdout)

	for _, want := range []string{"status", "version", "completion", "help", "Agent usage", "Exit levels"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("help output does not mention %q", want)
		}
	}
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}
	res.AssertNoLeaks(t)
}

// `igdev` with no arguments is the same as `igdev help`, not an error: a human
// typing the bare command gets oriented.
func TestBareInvocationShowsHelp(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun()
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.HasPrefix(res.Stdout, "igdev is the Ignition") {
		t.Errorf("bare invocation did not print the long help:\n%s", firstLines(res.Stdout, 3))
	}
}

// Per-command help carries the flags, an example, and the machine contract.
func TestHelpForStatus(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("help", "status")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stdout, "igdev status") || !strings.Contains(res.Stdout, "--json") {
		t.Errorf("status help is missing its usage or the --json flag:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "Examples:") {
		t.Errorf("status help has no examples section:\n%s", res.Stdout)
	}

	// The --help flag reaches the same text.
	flagRun := env.MustRun("version", "--help")
	testrig.WantExit(t, flagRun, contract.ExitOK)
	if !strings.Contains(flagRun.Stdout, "CLI Contract Version") {
		t.Errorf("version --help did not describe the CLI Contract Version:\n%s", flagRun.Stdout)
	}
}

// Asking about a command that does not exist is a usage error, not a help dump.
func TestHelpUnknownTopic(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("help", "gateway", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUsage)
	if !strings.Contains(envelope.Message, "gateway") {
		t.Errorf("message does not name the unknown topic: %q", envelope.Message)
	}
}

// Each supported shell gets a working script on stdout and writes nothing.
func TestCompletionScripts(t *testing.T) {
	env := testrig.NewEnv(t)
	markers := []struct{ shell, marker string }{
		{"bash", "# bash completion V2 for igdev"},
		{"zsh", "#compdef igdev"},
		{"fish", "# fish completion for igdev"},
		{"powershell", "# powershell completion for igdev"},
	}
	for _, tc := range markers {
		shell, marker := tc.shell, tc.marker
		t.Run(shell, func(t *testing.T) {
			res := env.MustRun("completion", shell)
			testrig.WantExit(t, res, contract.ExitOK)
			if !strings.Contains(res.Stdout, marker) {
				t.Errorf("%s script missing %q:\n%s", shell, marker, firstLines(res.Stdout, 10))
			}
			if res.Stderr != "" {
				t.Errorf("%s script wrote stderr: %q", shell, res.Stderr)
			}
			res.AssertNoLeaks(t)
		})
	}
}

// The completion script is data, so the machine dialect does not rewrite it.
func TestCompletionUnaffectedByJSONFlag(t *testing.T) {
	env := testrig.NewEnv(t)

	plain := env.MustRun("completion", "bash")
	machine := env.MustRun("completion", "bash", "--json")
	testrig.WantExit(t, plain, contract.ExitOK)
	testrig.WantExit(t, machine, contract.ExitOK)
	if plain.Stdout != machine.Stdout {
		t.Errorf("--json changed the completion script")
	}
}

// The hidden completion protocol resolves a partial command line, which is what
// the generated scripts drive.
func TestShellCompletionSuggestsCommands(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("__complete", "st")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stdout, "status") {
		t.Errorf("completion did not suggest status:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "completion") {
		t.Errorf("completion suggested an already-typed word:\n%s", res.Stdout)
	}
}

func firstLines(s string, n int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
