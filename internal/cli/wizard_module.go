package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The `module add` Wizard, in the frozen step sequence (3 steps, Q19): the
// artifact and its metadata, the notices a private module carries, and the
// confirmation to copy it. Enabling is not a step: a staged module is enabled by
// being staged (issue #34), so a question about the whitelist could not change
// anything.
const moduleAddWizardSteps = 3

// setupModuleAddWizard runs the `module add` steps that do not need the Project
// Contract: the artifact the invocation did not name, and the metadata it
// declares. The run's argument validation comes first, so an invocation that
// supplies nothing in Silent Mode still fails the same way it always has.
func (a *App) setupModuleAddWizard(cmd *cobra.Command, run wizardRun, file string) (string, error) {
	if !run.prompt {
		return file, nil
	}
	a.wizardBanner("module add", 1, moduleAddWizardSteps, "the artifact")
	if file == "" {
		if err := a.ask("module add", huh.NewInput().
			Title("Module archive").
			Description("Path to the .modl to stage in this checkout.").
			Value(&file).
			Validate(func(value string) error {
				if _, fault := modules.Validate(value); fault != nil {
					return errors.New(fault.Message)
				}
				return nil
			})); err != nil {
			return "", err
		}
	}
	return file, nil
}

// moduleAddWizardSteps runs the rest of the `module add` Wizard, once the Gate
// has admitted the checkout: what the artifact declares, what a private module
// carries with it, and the confirmation to copy.
func (a *App) moduleAddWizardSteps(_ *cobra.Command, run wizardRun, file string) error {
	if !run.prompt {
		return nil
	}
	record, fault := modules.Validate(file)
	if fault != nil {
		// The file is wrong, not the invocation: the same fault the silent path
		// reports, so the two dialects stay one contract.
		return fault
	}
	a.wizardNote("module:   %s %s (%s)", record.Name, record.Version, record.ID)
	if info, err := os.Stat(file); err == nil {
		a.wizardNote("artifact: %s (%d bytes)", record.Artifact, info.Size())
	} else {
		a.wizardNote("artifact: %s", record.Artifact)
	}

	// Step 2: what a private module carries. igdev reads metadata only (ADR
	// 0005), so the notices say what it does not check, and name the human-only
	// commands that record the terms a Gateway needs.
	a.wizardBanner("module add", 2, moduleAddWizardSteps, "the module notices")
	a.wizardNote("signature: igdev does not verify module signatures; stage only artifacts you trust")
	record2, _ := consent.Load(consent.Path(xdg.Resolve().Config))
	for _, term := range []consent.Term{consent.ModuleLicense, consent.ModuleCert} {
		if accepted, ok := record2.Accepted(term); ok {
			a.wizardNote("%s: accepted on this machine (accepted %s)", term.Title, accepted.AcceptedAt)
			continue
		}
		a.wizardNote("%s: not accepted on this machine; accept it with `igdev setup %s` (%s)",
			term.Title, term.Flag, term.Why)
	}

	// Step 3: the copy itself.
	a.wizardBanner("module add", 3, moduleAddWizardSteps, "the copy")
	// The copy is what the verb is for, so it is the default; only a person who
	// says no stops it.
	copyIt := true
	if err := a.ask("module add", huh.NewConfirm().
		Title(fmt.Sprintf("Copy %s into the checkout's module directory?", record.Artifact)).
		Description("The Gateway mounts that directory read-only on its next launch.").
		Affirmative("Copy it").
		Negative("Stop here").
		Value(&copyIt)); err != nil {
		return err
	}
	if !copyIt {
		return a.wizardDeclined("module add", fmt.Sprintf("%s was copied", record.Artifact))
	}
	return nil
}
