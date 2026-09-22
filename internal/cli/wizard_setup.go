package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/baseline"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/localconfig"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The setup Wizard, in the frozen step sequence (7 steps, Q19): the Consent
// gate, the Gateway heap, the admin password, the optional Baseline, the
// optional port pin, the materialization, and the summary.
const setupWizardSteps = 7

// setupWizard is what the setup Wizard decided, for the summary the run prints
// once the checkout is materialized.
type setupWizard struct {
	heapMB   int
	timezone string
	// password is where the admin password comes from: kept, generated, entered,
	// or empty when the Wizard left the decision to the silent path.
	password string
	baseline string
	pin      int
}

// setupNeedsWizard reports whether setup has something only a person can settle:
// a Checkout Setup that was never materialized, an admin password no checkout
// has generated yet, or a Consent term this machine has not accepted. Everything
// else is already on record, so a terminal gets the silent path.
func (a *App) setupNeedsWizard(found project.Found) bool {
	if !found.Setup || len(found.SetupRaw) == 0 {
		return true
	}
	if creds, err := localconfig.Load(found.LocalConfigTOML); err != nil || creds.Password == "" {
		return true
	}
	return consent.Check(consent.Path(xdg.Resolve().Config), consent.EULA) != nil
}

// runSetupWizard is setup's value source: the Consent gate, the heap, the admin
// password, the Baseline, and the port pin, each recorded as a flag value so the
// materialization below runs exactly once, unchanged.
func (a *App) runSetupWizard(cmd *cobra.Command, found project.Found, res *config.Resolution, flags wizardFlags, doc project.Doc) (*setupWizard, error) {
	run, err := a.decide("setup", flags, res.IsJSON(), a.setupNeedsWizard(found))
	if err != nil || !run.prompt {
		return nil, err
	}
	wizard := &setupWizard{heapMB: doc.Gateway.MemoryMB, timezone: doc.Gateway.Timezone}

	// Step 1: the Consent gate. igdev never accepts a legal term for a person
	// (ADR 0004), so this step shows the command that does and stops with the
	// same exit-3 fault Silent Mode returns until the record says otherwise.
	a.wizardBanner("setup", 1, setupWizardSteps, "the Consent gate")
	if err := a.setupConsentStep(); err != nil {
		return nil, err
	}

	// Step 2: the Gateway heap the contract asks for. It is a tracked value, so
	// the Wizard reports it instead of writing it.
	a.wizardBanner("setup", 2, setupWizardSteps, "the Gateway heap")
	a.wizardNote("igdev.toml requests %d MiB of Gateway heap (timezone %s)",
		wizard.heapMB, wizard.timezone)
	a.wizardNote("change it with `igdev init --gateway-memory-mb N`, then re-run `igdev setup`")

	// Step 3: the admin password.
	a.wizardBanner("setup", 3, setupWizardSteps, "the admin password")
	password, err := a.setupPasswordStep(cmd, found)
	if err != nil {
		return nil, err
	}
	wizard.password = password

	// Step 4: an optional Baseline.
	a.wizardBanner("setup", 4, setupWizardSteps, "the Baseline")
	a.wizardNote("optional: a .gwbk this checkout restores from with `igdev gateway reset`")
	baselinePath, err := a.setupBaselineStep()
	if err != nil {
		return nil, err
	}
	wizard.baseline = baselinePath
	if baselinePath != "" {
		if err := cmd.Flags().Set("baseline", baselinePath); err != nil {
			return nil, wizardFlagFault("baseline", err)
		}
	}

	// Step 5: an optional port pin, which is machine-local by design (ADR 0003)
	// and is never written to the tracked contract.
	a.wizardBanner("setup", 5, setupWizardSteps, "the port pin")
	a.wizardNote("optional: pin the Gateway HTTP port for this checkout on this machine")
	pin, err := a.setupPortStep(cmd)
	if err != nil {
		return nil, err
	}
	wizard.pin = pin

	// Step 6: the materialization itself, which the command below performs and
	// reports file by file.
	a.wizardBanner("setup", 6, setupWizardSteps, "the materialization")
	a.wizardNote("writing the Checkout Setup under %s", project.StateDir)
	if baselinePath != "" {
		a.wizardNote("staging the Baseline from %s", baselinePath)
	}
	return wizard, nil
}

// setupConsentStep gates the Wizard on the machine Consent record. A missing
// term never becomes an acceptance: the step shows the exact command a person
// runs, and the run stops with the frozen IGDEV_E_CONSENT_REQUIRED fault at exit
// level 3 until the record itself says the term is accepted.
func (a *App) setupConsentStep() error {
	consentPath := consent.Path(xdg.Resolve().Config)
	if fault := consent.Check(consentPath, consent.EULA); fault == nil {
		if record, err := consent.Load(consentPath); err == nil {
			if accepted, ok := record.Accepted(consent.EULA); ok {
				a.wizardNote("the Ignition EULA is accepted on this machine (accepted %s)", accepted.AcceptedAt)
			}
		}
		return nil
	}
	a.wizardNote("the Ignition EULA is not accepted on this machine yet")
	a.wizardNote("only a person may accept it: run `igdev setup --accept-eula` in a terminal")
	var accepted bool
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title("Has `igdev setup --accept-eula` been run on this machine?").
		Description("igdev re-reads the Consent record, so only the command itself counts.").
		Affirmative("Yes, re-read the record").
		Negative("Stop here").
		Value(&accepted))).
		WithInput(a.Stdin).
		WithOutput(a.Stderr)
	if err := form.Run(); err != nil && !errors.Is(err, huh.ErrUserAborted) {
		return wizardFault("setup", err)
	}
	// Whatever was answered, the record decides: a Wizard answer is never an
	// acceptance.
	return consent.Check(consentPath, consent.EULA)
}

// setupPasswordStep asks what to do about the Gateway admin password. Keeping a
// recorded password is the default, because re-running setup never rotates a
// credential a Gateway is already using.
func (a *App) setupPasswordStep(cmd *cobra.Command, found project.Found) (string, error) {
	existing, _ := localconfig.Load(found.LocalConfigTOML)
	choice := "generate"
	options := []huh.Option[string]{
		huh.NewOption("Generate one (24 characters, never printed)", "generate"),
		huh.NewOption("Enter one", "enter"),
	}
	if existing.Password != "" {
		choice = "keep"
		options = append([]huh.Option[string]{
			huh.NewOption("Keep the password this checkout already recorded", "keep"),
		}, options...)
	}
	if err := a.ask("setup", huh.NewSelect[string]().
		Title("Gateway admin password").
		Description("The password is stored 0600 in the Checkout Setup and never printed.").
		Options(options...).
		Value(&choice)); err != nil {
		return "", err
	}
	switch choice {
	case "keep":
		a.wizardNote("keeping the recorded password")
		return "keep", nil
	case "enter":
		var entered string
		if err := a.ask("setup", huh.NewInput().
			Title("Admin password").
			Description("Typed here, stored 0600, never echoed in output.").
			EchoMode(huh.EchoModePassword).
			Value(&entered)); err != nil {
			return "", err
		}
		if entered == "" {
			a.wizardNote("empty password ignored; the Web GUI password stays unset")
			return "unchanged", nil
		}
		if err := cmd.Flags().Set("admin-password", entered); err != nil {
			return "", wizardFlagFault("admin-password", err)
		}
		return "entered", nil
	default:
		generated, err := localconfig.GeneratePassword()
		if err != nil {
			return "", contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				fmt.Sprintf("cannot generate the Gateway admin password: %v", err)).WithCause(err)
		}
		if err := cmd.Flags().Set("admin-password", generated); err != nil {
			return "", wizardFlagFault("admin-password", err)
		}
		a.wizardNote("a new password was generated into the Checkout Setup")
		return "generated", nil
	}
}

// setupBaselineStep asks for an optional Baseline backup, and refuses a path that
// could not be staged rather than letting the run fail after every other answer.
func (a *App) setupBaselineStep() (string, error) {
	var path string
	if err := a.ask("setup", huh.NewInput().
		Title("Baseline backup (optional)").
		Description("Path to a .gwbk, or empty to restore nothing.").
		Value(&path).
		Validate(func(value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil
			}
			if fault := baseline.Validate(value); fault != nil {
				return errors.New(fault.Message)
			}
			return nil
		})); err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

// setupPortStep asks for an optional Gateway HTTP port pin. The pin is recorded
// machine-locally, so it changes this checkout on this machine and nothing the
// repository tracks.
func (a *App) setupPortStep(cmd *cobra.Command) (int, error) {
	var entered string
	if err := a.ask("setup", huh.NewInput().
		Title("Gateway HTTP port (optional)").
		Description("A port to pin for this checkout, or empty to allocate one.").
		Value(&entered).
		Validate(func(value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil
			}
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("%q is not a port between 1 and 65535", value)
			}
			return nil
		})); err != nil {
		return 0, err
	}
	entered = strings.TrimSpace(entered)
	if entered == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(entered)
	if err != nil {
		return 0, wizardFlagFault("gateway-port", err)
	}
	if err := cmd.Flags().Set("gateway-port", entered); err != nil {
		return 0, wizardFlagFault("gateway-port", err)
	}
	a.wizardNote("pinning the Gateway HTTP port to %d (recorded in %s)", port, project.LocalConfig)
	return port, nil
}

// summarizeSetupWizard is the Wizard's closing step: what it decided, and what
// the checkout does next. It runs after the materialization, so the ports it
// names are the ones the run recorded.
func (a *App) summarizeSetupWizard(wizard *setupWizard, data setupData) {
	if wizard == nil {
		return
	}
	a.wizardBanner("setup", setupWizardSteps, setupWizardSteps, "the summary")
	a.wizardNote("instance %s on http://%s:%d (ports %v)",
		data.Instance, ports.BindAddress, data.Ports.HTTP, data.Ports.All())
	if wizard.password != "" && wizard.password != "keep" {
		a.wizardNote("admin password: %s (read it with `igdev gateway credentials --json`)", wizard.password)
	} else {
		a.wizardNote("admin password: %s", data.Credentials.Source)
	}
	if wizard.pin > 0 {
		a.wizardNote("port pin: %d, recorded in %s", wizard.pin, project.LocalConfig)
	}
	if wizard.baseline != "" {
		a.wizardNote("baseline: staged from %s", wizard.baseline)
	}
	a.wizardNote("next: `igdev gateway up`")
}

func wizardFlagFault(flag string, err error) *contract.Fault {
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
		fmt.Sprintf("cannot apply the Wizard's %s answer: %v", flag, err)).WithCause(err)
}
