package cli

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	xterm "github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/contract"
)

// The Wizard interaction policy, frozen by the spec's Q6: igdev prompts only
// when a required value is missing, stdin is a terminal, and neither --json nor
// --yes was asked for. --interactive forces the Wizard even when every value was
// already supplied; --yes takes the Wizard's defaults and never prompts.
//
// A Wizard is only possible on a terminal — it has no way to ask an automated
// invocation anything — so a missing value without a terminal stays
// IGDEV_E_MISSING_ARGUMENT at exit 2, which is the fault an agent already
// handles.
//
// A Wizard is a value source, never a second pipeline: every answer it collects
// becomes the flag value the silent path would have been given, and the command
// below the prompt layer runs exactly once, unchanged. That is what makes
// `init --yes`, `init --json`, and `init` with every flag agree byte for byte.

// wizardFlags are the two interaction flags every Wizard verb carries.
type wizardFlags struct {
	interactive bool
	yes         bool
}

// register adds --interactive and --yes to a Wizard verb.
func (w *wizardFlags) register(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.BoolVarP(&w.interactive, "interactive", "i", false,
		"run the Wizard even when every value is already supplied")
	flags.BoolVarP(&w.yes, "yes", "y", false,
		"take the Wizard's defaults and never prompt")
}

// wizardRun is what the policy decided for one invocation.
type wizardRun struct {
	// prompt runs the Wizard's steps: a person answers them.
	prompt bool
	// adopt fills the values the invocation did not state with the defaults the
	// Wizard offers. --yes is adopt without prompt.
	adopt bool
}

// decide applies the frozen policy. jsonMode reports that this invocation asked
// for the machine dialect, and missing reports whether this verb has a required
// value the invocation did not state, which is the only thing that opens a Wizard
// unasked.
func (a *App) decide(verb string, flags wizardFlags, jsonMode bool, missing bool) (wizardRun, error) {
	// --json is Silent Mode whatever the terminal is: machine output never waits
	// on a person, so the Wizard never runs and never fills a default.
	if jsonMode {
		return wizardRun{}, nil
	}
	if flags.interactive && flags.yes {
		return wizardRun{}, contract.UsageFault(
			fmt.Sprintf("igdev %s cannot combine --interactive with --yes: one asks for prompts, the other for none", verb),
			contract.Remediation{Command: "igdev " + verb + " --yes", Why: "take every default without prompting"},
			contract.Remediation{Command: "igdev " + verb + " --interactive", Why: "answer every step in a terminal"})
	}
	if flags.yes {
		return wizardRun{adopt: true}, nil
	}
	if flags.interactive && !a.stdinTerminal() {
		return wizardRun{}, contract.UsageFault(
			fmt.Sprintf("igdev %s --interactive needs a terminal on stdin: a Wizard has no way to ask", verb),
			contract.Remediation{Command: "igdev help " + verb, Why: "supply every value with flags instead"},
			contract.Remediation{Command: "igdev " + verb, Why: "run it in a terminal when a person can answer"})
	}
	return wizardRun{
		prompt: a.stdinTerminal() && (flags.interactive || missing),
		adopt:  true,
	}, nil
}

// stdinTerminal reports whether this invocation has a person on the other end of
// stdin. It is the gate the whole policy turns on: no terminal, no Wizard.
func (a *App) stdinTerminal() bool {
	return a.Stdin != nil && xterm.IsTerminal(a.Stdin.Fd())
}

// wizardBanner announces one Wizard step on stderr. It is the transcript a
// person reads and the step sequence the golden tests freeze; it goes to stderr
// because stdout carries the command's own result, which a pipe still has to
// receive intact.
func (a *App) wizardBanner(verb string, step, total int, subject string) {
	fmt.Fprintf(a.Stderr, "[igdev] %s wizard %d/%d: %s\n", verb, step, total, subject)
}

// wizardNote prints one Wizard step's finding on stderr, in the shape a person
// reads a step that asks nothing.
func (a *App) wizardNote(format string, args ...any) {
	fmt.Fprintf(a.Stderr, "[igdev]   "+format+"\n", args...)
}

// ask runs one step's fields as a form on this invocation's terminal. Each step
// is its own form so a step that has to stop the run — a missing Consent term —
// stops it before the next question is asked.
func (a *App) ask(verb string, fields ...huh.Field) error {
	form := huh.NewForm(huh.NewGroup(fields...)).
		WithInput(a.Stdin).
		WithOutput(a.Stderr)
	if err := form.Run(); err != nil {
		return wizardFault(verb, err)
	}
	return nil
}

// wizardFault translates what a form returned: a person stopping the Wizard is
// an invocation that supplied nothing, and anything else is an igdev failure.
func wizardFault(verb string, err error) *contract.Fault {
	if errors.Is(err, huh.ErrUserAborted) {
		return wizardStopped(verb, "the Wizard was answered")
	}
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
		fmt.Sprintf("the igdev %s Wizard cannot run: %v", verb, err)).WithCause(err)
}

// wizardStopped is the fault every stopped Wizard returns: a declined
// confirmation, a Ctrl-C, a step that could not be completed. Nothing was
// supplied that the invocation could act on, so it carries the missing-value
// code at usage level, and the message names what did not happen.
func wizardStopped(verb, subject string) *contract.Fault {
	return contract.NewFault(contract.CodeMissingArgument, contract.ExitUsage,
		fmt.Sprintf("igdev %s: the Wizard stopped before %s; nothing was written", verb, subject)).
		WithRemediation(contract.Remediation{
			Command: "igdev help " + verb,
			Why:     "see every value this command takes, so it can run without a Wizard",
		})
}

// wizardDeclined stops a Wizard whose confirmation a person answered no. It is
// the same stop, named after the decision rather than after the value.
func (a *App) wizardDeclined(verb, subject string) error {
	a.wizardNote("declined: nothing was written")
	return wizardStopped(verb, subject)
}

// setFlags records a Wizard's answers as flag values, so everything below the
// prompt layer reads them exactly as though they had been typed. A value the
// invocation already stated is never overwritten unless forced is set, which is
// what makes --interactive show the supplied value preselected instead of
// discarding it.
func setFlags(cmd *cobra.Command, forced bool, values map[string]string) error {
	for name, value := range values {
		if !forced && cmd.Flags().Changed(name) {
			continue
		}
		if err := cmd.Flags().Set(name, value); err != nil {
			return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				fmt.Sprintf("cannot apply the Wizard's %s answer: %v", name, err)).WithCause(err)
		}
	}
	return nil
}
