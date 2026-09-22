package cli

import (
	"fmt"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// missingArgument is the frozen way to say "you have to supply this": exit 2
// with IGDEV_E_MISSING_ARGUMENT, naming the argument and one worked command.
func missingArgument(command, argName, example, why string) *contract.Fault {
	return contract.NewFault(contract.CodeMissingArgument, contract.ExitUsage,
		fmt.Sprintf("igdev %s needs the <%s> argument", command, argName)).
		WithRemediation(contract.Remediation{Command: example, Why: why})
}

// missingFlag is the frozen way to say "you have to supply this flag": exit 2
// with IGDEV_E_MISSING_ARGUMENT, naming the flag and one worked command.
func missingFlag(command, flag, example, why string) *contract.Fault {
	return contract.NewFault(contract.CodeMissingArgument, contract.ExitUsage,
		fmt.Sprintf("igdev %s needs the %s flag", command, flag)).
		WithRemediation(contract.Remediation{Command: example, Why: why})
}

// extraArguments reports arguments a command cannot use.
func extraArguments(command string, want int, args []string) *contract.Fault {
	return contract.UsageFault(
		fmt.Sprintf("igdev %s takes %s, got %s", command, argCount(want), quoteArgs(args)),
		contract.Remediation{
			Command: "igdev help " + command,
			Why:     "show the arguments and flags this command takes",
		})
}

func argCount(want int) string {
	switch {
	case want == 0:
		return "no arguments"
	case want == 1:
		return "1 argument"
	default:
		return fmt.Sprintf("%d arguments", want)
	}
}

func quoteArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprintf("%q", a)
	}
	return "[" + strings.Join(parts, " ") + "]"
}
