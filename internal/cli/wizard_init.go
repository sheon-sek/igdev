package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/project"
)

// The init Wizard, in the frozen step sequence (7 steps, Q19): the project
// stack, the Ignition version, the modules (all or only the picked ones, and on
// a Gradle or Maven layout the artifacts the build writes), the scan paths, the
// command strings, the Gateway options, and the summary of what is written.
const initWizardSteps = 7

// initNeedsWizard reports whether init has values to invent: a repository with
// no Project Contract yet, or one this binary cannot carry forward, has nothing
// to preserve, so a terminal gets the Wizard without being asked twice. A
// contract init can read is an edit, and an edit is the silent path.
func initNeedsWizard(found project.Found) bool {
	if len(found.ContractTOML) == 0 {
		return true
	}
	if project.DeclaredSchema(found.ContractTOML) != project.LatestSchema {
		return true
	}
	_, err := project.DecodeDoc(found.ContractTOML)
	return err != nil
}

// runInitWizard is init's value source. It reads the repository's layout, asks a
// person for the values when the policy allows it, and records every answer as a
// flag value, so the command below runs the same path an invocation with those
// flags spelled out runs.
//
// current resolves the contract the run would write, which is what the steps
// pre-select and what the closing summary reports.
func (a *App) runInitWizard(cmd *cobra.Command, found project.Found, res *config.Resolution, flags wizardFlags, current func() project.Doc) error {
	run, err := a.decide("init", flags, res.IsJSON(), initNeedsWizard(found))
	if err != nil || (!run.prompt && !run.adopt) {
		return err
	}
	detected := detectStack(found.Root)
	doc := current()

	// Step 1: the project stack. What it finds is the default for [commands] and
	// [scan], and what the file already declares wins over it.
	stack := detected.Stack
	if run.prompt {
		a.wizardBanner("init", 1, initWizardSteps, "the project stack")
		if detected.Marker != "" {
			a.wizardNote("detected the %s layout from %s", stackTitle(detected.Stack), detected.Marker)
		} else {
			a.wizardNote("no Gradle, Maven, npm, or Python marker found; keeping the schema defaults")
		}
		if err := a.ask("init", huh.NewSelect[Stack]().
			Title("Project stack").
			Description("The stack whose stages pre-fill [commands].").
			Options(stackOptions()...).
			Value(&stack)); err != nil {
			return err
		}
	}

	// Step 2: the Ignition version, from the Core Catalog this binary carries.
	versions := catalog.Versions()
	version := doc.Ignition.Version
	if !slices.Contains(versions, version) {
		version = catalog.Target
	}
	if run.prompt && len(versions) > 0 {
		a.wizardBanner("init", 2, initWizardSteps, "the Ignition version")
		if err := a.ask("init", huh.NewSelect[string]().
			Title("Ignition version").
			Description("The version this repository targets; igdev carries a Core Catalog for it.").
			Options(huh.NewOptions(versions...)...).
			Value(&version)); err != nil {
			return err
		}
	}

	// Step 3: the modules. First which built-in modules load: all of them (the
	// default, an empty whitelist) or only the ones a person picks. A whitelist
	// entry the catalog does not carry — a private module staged from an
	// artifact — is offered too, so an edit run never drops what the file
	// already names. Staged private modules load in either mode. On a Gradle or
	// Maven layout the step also asks which module artifacts the build writes.
	enabled := doc.Modules.Enabled
	artifacts, askArtifacts := artifactDefault(stack, detected, doc.Modules.Artifacts)
	if run.prompt {
		options, err := moduleOptions(version, enabled)
		if err != nil {
			return err
		}
		a.wizardBanner("init", 3, initWizardSteps, "the modules")
		mode := moduleModeAll
		if len(enabled) > 0 {
			mode = moduleModePick
		}
		if err := a.ask("init", huh.NewSelect[string]().
			Title("Modules to load").
			Description("Staged custom modules always load, whichever you choose.").
			Options(
				huh.NewOption("All modules (default)", moduleModeAll),
				huh.NewOption("Only the modules I pick", moduleModePick),
			).
			Value(&mode)); err != nil {
			return err
		}
		if mode == moduleModeAll {
			enabled = nil
		} else {
			returned := append([]string{}, enabled...)
			if err := a.ask("init", huh.NewMultiSelect[string]().
				Title("Enabled modules").
				Description("Space selects, Enter confirms. Picking none loads every module.").
				Options(options...).
				Value(&returned)); err != nil {
				return err
			}
			enabled = returned
		}
		if askArtifacts {
			if err := a.ask("init", huh.NewInput().
				Title("[modules].artifacts").
				Description("The module artifacts the build writes, comma-separated globs; "+
					"`igdev build` stages every match. Empty stages none.").
				Placeholder(stackArtifacts[stack]).
				Value(&artifacts)); err != nil {
				return err
			}
		}
	}

	// Step 4: the scan paths, at the schema default or the directories the
	// repository actually has.
	scanJython := declaredOr(doc.Scan.Jython, detected.Scan)
	scanCapabilities := declaredOr(doc.Scan.Capabilities, detected.Scan)
	if run.prompt {
		a.wizardBanner("init", 4, initWizardSteps, "the scan paths")
		a.wizardNote("the schema default is %s", strings.Join(project.DefaultScanPaths, ", "))
		fields := []huh.Field{huh.NewInput().
			Title("[scan].jython").
			Description("Directories scanned for Jython sources, comma-separated.").
			Value(&scanJython)}
		// The two lists are usually the same list; they are only asked apart when
		// the file already states them differently, so an edit never flattens one
		// into the other.
		if !slices.Equal(doc.Scan.Jython, doc.Scan.Capabilities) {
			fields = append(fields, huh.NewInput().
				Title("[scan].capabilities").
				Description("Directories scanned for capability usage, comma-separated.").
				Value(&scanCapabilities))
		}
		if err := a.ask("init", fields...); err != nil {
			return err
		}
	}

	// Step 5: the command strings, at the layout's stages or the file's own.
	commands := mergedCommands(detected.Commands, doc.Commands)
	if run.prompt {
		a.wizardBanner("init", 5, initWizardSteps, "the command strings")
		if err := a.ask("init",
			huh.NewInput().Title("[commands].check").
				Description("What `igdev check` runs for this project.").Value(&commands.Check),
			huh.NewInput().Title("[commands].test").
				Description("What `igdev test` runs for this project.").Value(&commands.Test),
			huh.NewInput().Title("[commands].build").
				Description("What `igdev build` runs for this project.").Value(&commands.Build),
		); err != nil {
			return err
		}
	}

	// Step 6: the Gateway options — the one contract value that changes what the
	// Gateway loads. The default is what the contract already holds, which is
	// yes unless it states false, so an edit never flips it.
	allowUnsignedModules := doc.Gateway.UnsignedModulesAllowed()
	if run.prompt {
		a.wizardBanner("init", 6, initWizardSteps, "the Gateway options")
		a.wizardNote("a module repository builds unsigned artifacts until it has a signing key; " +
			"a development Gateway loads them unless the contract refuses it")
		if err := a.ask("init", huh.NewConfirm().
			Title("Allow unsigned modules").
			Description("Render IGNITION_ALLOW_UNSIGNED_MODULES for this Instance's Gateway.").
			Affirmative("Yes").Negative("No").
			Value(&allowUnsignedModules)); err != nil {
			return err
		}
	}

	answers := map[string]string{
		"ignition-version":       version,
		"modules":                strings.Join(enabled, ","),
		"scan-jython":            scanJython,
		"scan-capabilities":      scanCapabilities,
		"command-check":          commands.Check,
		"command-test":           commands.Test,
		"command-build":          commands.Build,
		"allow-unsigned-modules": strconv.FormatBool(allowUnsignedModules),
	}
	if askArtifacts {
		answers["modules-artifacts"] = artifacts
	}
	if err := setFlags(cmd, run.prompt, answers); err != nil {
		return err
	}

	// Step 7: what the run is about to write, and then the write itself.
	if run.prompt {
		a.wizardBanner("init", 7, initWizardSteps, "the summary")
		a.printInitSummary(current())
	}
	return nil
}

// printInitSummary shows the contract the Wizard is about to write, in the
// vocabulary a person reads. The write below prints the unified diff, so this is
// the last look before the file moves.
func (a *App) printInitSummary(doc project.Doc) {
	a.wizardNote("contract:  %s (Ignition %s, Jython %s, edition %s)",
		project.ContractFile, doc.Ignition.Version, doc.Ignition.JythonVersion, doc.Ignition.Edition)
	if len(doc.Modules.Enabled) == 0 {
		a.wizardNote("modules:   all (every built-in module loads)")
	} else {
		a.wizardNote("modules:   only %s", strings.Join(doc.Modules.Enabled, ", "))
	}
	if !doc.Modules.ArtifactsEmpty() {
		a.wizardNote("artifacts: %s (staged by `igdev build`)", strings.Join(doc.Modules.Artifacts, ", "))
	}
	a.wizardNote("scan:      jython %s; capabilities %s",
		listOr(doc.Scan.Jython, "none"), listOr(doc.Scan.Capabilities, "none"))
	a.wizardNote("gateway:   unsigned modules %s", allowedWord(doc.Gateway.UnsignedModulesAllowed()))
	if doc.Commands.Empty() {
		a.wizardNote("commands:  none declared")
		return
	}
	a.wizardNote("commands:  check %s; test %s; build %s",
		stageOr(doc.Commands.Check), stageOr(doc.Commands.Test), stageOr(doc.Commands.Build))
}

// The module step's two modes.
const (
	moduleModeAll  = "all"
	moduleModePick = "pick"
)

// artifactDefault decides whether the module step asks for [modules].artifacts
// and what it pre-fills: only a Gradle or Maven layout is asked, what the
// contract already declares wins, and the layout's glob is pre-filled only when
// a build file declares an Ignition module plugin, because a glob that matches
// nothing fails `igdev build`.
func artifactDefault(stack Stack, detected detection, declared []string) (string, bool) {
	glob, ok := stackArtifacts[stack]
	if !ok {
		return "", false
	}
	if len(declared) > 0 {
		return strings.Join(declared, ","), true
	}
	if detected.Stack == stack && detected.BuildsModules {
		return glob, true
	}
	return "", true
}

// moduleOptions renders the module whitelist step: every built-in module of the
// version, labelled with what it provides, followed by any whitelisted id the
// catalog does not carry.
func moduleOptions(version string, enabled []string) ([]huh.Option[string], error) {
	core, fault := catalog.Core(version)
	if fault != nil {
		return nil, fault
	}
	options := make([]huh.Option[string], 0, len(core.BuiltinModules)+len(enabled))
	known := make([]string, 0, len(core.BuiltinModules))
	for _, module := range core.BuiltinModules {
		options = append(options, huh.NewOption(module.ID+"  "+moduleSummary(module), module.ID))
		known = append(known, module.ID)
	}
	for _, id := range enabled {
		if slices.Contains(known, id) {
			continue
		}
		options = append(options, huh.NewOption(id, id))
	}
	return options, nil
}

// moduleSummary is the short description a module option carries: the module's
// own artifact name, which the image ships and the catalog records, with the
// packaging suffix the reader does not need taken off.
func moduleSummary(module catalog.BuiltinModule) string {
	name := strings.TrimSuffix(module.Artifact, ".modl")
	for _, suffix := range []string{"-module", " module", "-Module"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.ReplaceAll(name, "-", " ")
	if name == "" {
		return "(module)"
	}
	return name
}

// stackOptions are the stack choices, in the order the detection prefers them.
func stackOptions() []huh.Option[Stack] {
	return []huh.Option[Stack]{
		huh.NewOption("Gradle", StackGradle),
		huh.NewOption("Maven", StackMaven),
		huh.NewOption("npm", StackNPM),
		huh.NewOption("Python", StackPython),
		huh.NewOption("None (keep the schema defaults)", StackNone),
	}
}

// stackTitle is the human name of a stack.
func stackTitle(stack Stack) string {
	switch stack {
	case StackGradle:
		return "Gradle"
	case StackMaven:
		return "Maven"
	case StackNPM:
		return "npm"
	case StackPython:
		return "Python"
	default:
		return "unknown"
	}
}

// mergedCommands takes the stages the contract already declares over the ones
// the layout suggests, field by field: an edit keeps what the file says.
func mergedCommands(detected, declared project.Commands) project.Commands {
	out := detected
	if declared.Check != "" {
		out.Check = declared.Check
	}
	if declared.Test != "" {
		out.Test = declared.Test
	}
	if declared.Build != "" {
		out.Build = declared.Build
	}
	return out
}

// declaredOr prefers a list the contract states over a suggested one.
func declaredOr(declared, suggested []string) string {
	if len(declared) > 0 {
		return strings.Join(declared, ",")
	}
	return strings.Join(suggested, ",")
}

func listOr(values []string, empty string) string {
	if len(values) == 0 {
		return empty
	}
	return strings.Join(values, ", ")
}

func stageOr(stage string) string {
	if stage == "" {
		return "none"
	}
	return fmt.Sprintf("%q", stage)
}
