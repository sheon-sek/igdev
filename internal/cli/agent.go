package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/agentskill"
	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/runtimeassets"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The frozen Setup-state vocabulary `agent context` reports. `none` is a
// checkout with no Project Contract at all; the other three are the Gate's
// Setup Stamp verdicts.
const (
	setupStateNone     = "none"
	setupStateRequired = "required"
	setupStateStale    = "stale"
	setupStateCurrent  = "current"
)

// The frozen --scope values `agent skill-install` accepts.
const (
	scopeGlobal = "global"
	scopeRepo   = "repo"
)

// agentContextData is the `data` member of a successful `igdev agent context`
// envelope: one call's orientation. The key set and order are frozen by the
// goldens in itest/testdata/golden, and it is reported in every lifecycle
// state, initialized or not.
type agentContextData struct {
	ProjectRoot string         `json:"project_root"`
	Lifecycle   agentLifecycle `json:"lifecycle"`
	Versions    agentVersions  `json:"versions"`
	// Instance is null before setup has minted one; Gateway is null whenever
	// there is no Instance to describe.
	Instance *agentInstance `json:"instance"`
	Gateway  *agentGateway  `json:"gateway"`
	Modules  agentModules   `json:"modules"`

	Catalog      agentCatalog      `json:"catalog"`
	Capabilities agentCapabilities `json:"capabilities"`
	Commands     agentCommands     `json:"commands"`
}

// agentLifecycle is where this checkout sits in the lifecycle, and which legal
// terms the machine has accepted (ADR 0004). Consent is reported, never
// enforced: creating it is a human action.
type agentLifecycle struct {
	Initialized bool         `json:"initialized"`
	SetupState  string       `json:"setup_state"`
	Consent     agentConsent `json:"consent"`
}

// agentConsent reports each machine-global term's acceptance.
type agentConsent struct {
	IgnitionEULA      bool `json:"ignition_eula"`
	ModuleLicense     bool `json:"module_license"`
	ModuleCertificate bool `json:"module_certificate"`
}

// agentVersions is the version quartet an agent needs to reason about
// compatibility: the release binary, the CLI Contract it speaks, and the
// Ignition and Jython versions this checkout targets.
type agentVersions struct {
	CLI         string `json:"cli"`
	CLIContract string `json:"cli_contract"`
	Ignition    string `json:"ignition"`
	Jython      string `json:"jython"`
}

// agentInstance is the recorded Instance identity and its allocated ports.
type agentInstance struct {
	ID        string        `json:"id"`
	Namespace string        `json:"namespace"`
	Ports     ports.Triplet `json:"ports"`
}

// agentGateway is the Gateway's runtime state, read from the recorded URL and
// the container engine. Context never starts anything.
type agentGateway struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
}

// agentModules reports the private module artifacts this checkout stages, and
// what the contract lets the Gateway load and accept.
type agentModules struct {
	Count  int      `json:"count"`
	Staged []string `json:"staged"`
	// AllowUnsignedModules is the effective [gateway] allow_unsigned_modules:
	// true means the Gateway this checkout starts loads a module artifact that
	// carries no valid signature. It is a contract value, so it is reported in
	// every lifecycle state, initialized or not.
	AllowUnsignedModules bool `json:"allow_unsigned_modules"`
	// AutoAccepted lists the staged private module ids igdev passes to the Gateway
	// as ACCEPT_MODULE_LICENSES and ACCEPT_MODULE_CERTS without a human step
	// (ADR 0006). It is empty when the contract requires private-module Consent:
	// there, the ids are passed only after a person recorded the terms.
	AutoAccepted []string `json:"auto_accepted"`
	// RequirePrivateModuleConsent is the effective
	// [modules] require_private_module_consent, which is why AutoAccepted is empty.
	RequirePrivateModuleConsent bool `json:"require_private_module_consent"`
}

// agentCatalog is the Effective Catalog's identity: the Core Catalog digest and
// the Project Overlay's presence, digest, and declared files.
type agentCatalog struct {
	CoreDigest string            `json:"core_digest"`
	Overlay    agentCatalogLayer `json:"overlay"`
}

// agentCatalogLayer reports the tracked Project Overlay layer.
type agentCatalogLayer struct {
	Present bool     `json:"present"`
	Digest  string   `json:"digest"`
	Paths   []string `json:"paths"`
}

// agentCapabilities is the Effective Catalog's shape: the resolved row counts.
// The counts are for a reader; the catalog digests are the integrity.
type agentCapabilities struct {
	NativeFunctions int `json:"native_functions"`
	RestOperations  int `json:"rest_operations"`
	// OverlayRows is how many rows the Project Overlay contributes across every
	// plane.
	OverlayRows int `json:"overlay_rows"`
}

// agentCommands answers, per project work verb, whether it can do anything here.
// The pipeline stages (check, test, build) are project-declared: an undeclared
// stage is skipped, so false says the Project Contract does not wire it up. The
// capability verbs are provided by this binary, so their boolean is the CLI
// surface itself, without parsing help.
type agentCommands struct {
	Check    bool `json:"check"`
	Test     bool `json:"test"`
	Build    bool `json:"build"`
	Verify   bool `json:"verify"`
	Gateway  bool `json:"gateway"`
	Module   bool `json:"module"`
	Baseline bool `json:"baseline"`
	CILocal  bool `json:"ci_local"`
}

// AgentContextField documents one member of the `igdev agent context --json`
// data object: the JSON key, its shape, and what a reader does with it.
type AgentContextField struct {
	// Name is the JSON key, exactly as the envelope spells it. A nested member
	// is written as its path from the data root, e.g. lifecycle.setup_state.
	Name string
	// Type is the JSON shape: string, bool, int, object, array, or null.
	Type string
	// Description is what the member means and how to use it.
	Description string
}

// AgentContextFields is the frozen shape of `igdev agent context --json`, member
// by member: the source `docs/reference/agent-context.md` is generated from, so
// the documented field list cannot drift from the envelope. TestAgentContextFields
// checks every name against the JSON tags of the agentContextData struct, which
// is the compile-time definition.
var AgentContextFields = []AgentContextField{
	{"project_root", "string", "Absolute path of the Project Root, or empty when the working directory is outside one."},
	{"lifecycle", "object", "Where the checkout sits in the lifecycle and what this machine has accepted."},
	{"lifecycle.initialized", "bool", "A Project Contract (igdev.toml) was found by searching upward."},
	{"lifecycle.setup_state", "string", "none, required, stale, or current: none is no contract at all; the other three are the Gate's Setup Stamp verdicts. Only current admits the project verbs."},
	{"lifecycle.consent", "object", "Per-term machine Consent. It is reported here, never enforced: creating it is a human action (ADR 0004)."},
	{"lifecycle.consent.ignition_eula", "bool", "The Ignition EULA is accepted on this machine."},
	{"lifecycle.consent.module_license", "bool", "The module licenses are accepted on this machine."},
	{"lifecycle.consent.module_certificate", "bool", "The third-party module certificates are accepted on this machine."},
	{"versions", "object", "The version quartet to reason about compatibility with."},
	{"versions.cli", "string", "The release semver of this binary."},
	{"versions.cli_contract", "string", "The CLI Contract Version the envelope, the IGDEV_E_* codes, and the exit levels are frozen against."},
	{"versions.ignition", "string", "The Ignition version this checkout targets, after config precedence."},
	{"versions.jython", "string", "The Jython version the compatibility checker targets."},
	{"instance", "object", "The recorded Instance identity and allocated ports; null before setup."},
	{"instance.id", "string", "The Instance UUID minted at first setup, stable across directory moves."},
	{"instance.namespace", "string", "The Docker namespace igdev-<short-id> this Instance's resources use."},
	{"instance.ports", "object", "The allocated loopback ports: http, https, and debug. Always read these; never assume 8088 (ADR 0003)."},
	{"gateway", "object", "Whether the Gateway is running and its recorded URL; null when there is no Instance. Context never starts anything."},
	{"gateway.running", "bool", "The container engine reports this Instance's Gateway running."},
	{"gateway.url", "string", "The recorded Gateway URL, built from the Instance's allocated HTTP port."},
	{"modules", "object", "The private module artifacts this checkout stages, and what the contract lets the Gateway load."},
	{"modules.count", "int", "How many artifacts are staged in .igdev/modules/."},
	{"modules.staged", "array", "The module ids those artifacts declare."},
	{"modules.allow_unsigned_modules", "bool", "The effective [gateway] allow_unsigned_modules: true means the Gateway loads a module artifact that carries no valid signature. A contract value, so it is reported whether or not this checkout was set up."},
	{"modules.auto_accepted", "array", "The staged private module ids igdev passes to the Gateway as ACCEPT_MODULE_LICENSES and ACCEPT_MODULE_CERTS without a human step. Empty when the contract requires private-module Consent."},
	{"modules.require_private_module_consent", "bool", "The effective [modules] require_private_module_consent: true means those ids are passed only after a human recorded the module-license and module-cert terms (ADR 0006)."},
	{"catalog", "object", "The Effective Catalog's identity."},
	{"catalog.core_digest", "string", "sha256 of the Core Catalog embedded in this binary, for this Ignition version."},
	{"catalog.overlay", "object", "This repository's tracked Project Overlay layer."},
	{"catalog.overlay.present", "bool", "The contract declares overlay files that resolved."},
	{"catalog.overlay.digest", "string", "sha256 over the declared overlay files; the empty-input digest when none resolved."},
	{"catalog.overlay.paths", "array", "The overlay files, in contract declaration order."},
	{"capabilities", "object", "The Effective Catalog's shape. The counts are for a reader; the digests are the integrity."},
	{"capabilities.native_functions", "int", "Gateway-scope system.* functions the Effective Catalog resolves."},
	{"capabilities.rest_operations", "int", "REST operations the Effective Catalog resolves."},
	{"capabilities.overlay_rows", "int", "Rows the Project Overlay contributes across every plane."},
	{"commands", "object", "Per project verb, whether it can do anything in this checkout. The pipeline stages are project-declared, so false says the contract does not wire that stage up."},
	{"commands.check", "bool", "The contract declares [commands].check."},
	{"commands.test", "bool", "The contract declares [commands].test."},
	{"commands.build", "bool", "The contract declares [commands].build."},
	{"commands.verify", "bool", "Always true: verify is provided by this binary."},
	{"commands.gateway", "bool", "Always true: the gateway verbs are provided by this binary."},
	{"commands.module", "bool", "Always true: the capability verbs are provided by this binary."},
	{"commands.baseline", "bool", "Always true: the baseline verbs are provided by this binary."},
	{"commands.ci_local", "bool", "Always true: ci-local is provided by this binary."},
}

func (a *App) newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent-facing surface: one-call orientation and skill installation",
		Long: `agent is the surface built for coding agents rather than people.

` + "`agent context`" + ` returns everything an agent needs to orient in an unknown
repository in one call: the lifecycle state, the versions in play, the recorded
Instance and its ports, the Gateway's runtime state, the staged modules, the
capability catalog's identity, and which project verbs are available. It works
before ` + "`init`" + ` and ` + "`setup`" + `, and never mutates anything.

` + "`agent skill-install`" + ` writes the thin Agent Skill that matches this binary —
globally by default, or into the repository with ` + "`--scope repo`" + `.

Neither verb ever prompts: they are for Silent Mode.`,
		Example: `  igdev agent context --json
  igdev agent skill-install
  igdev agent skill-install --scope repo`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newAgentContextCmd(), a.newAgentSkillInstallCmd())
	return cmd
}

func (a *App) newAgentContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Report one repository's orientation in a single call",
		Long: `context is the one call an agent makes to orient itself. It reports, as a frozen
envelope:

  lifecycle     initialized, the Setup Stamp state (none, required, stale, or
                current), and the machine Consent terms
  versions      the CLI release, the CLI Contract Version, and the targeted
                Ignition and Jython versions
  instance      the recorded Instance identity and its ports, or null before setup
  gateway       whether the Gateway is running and its recorded URL, or null when
                there is no Instance (context never starts anything)
  modules       the staged private module artifacts, and whether the contract
                lets the Gateway load an unsigned one
  catalog       the Core Catalog digest and the Project Overlay layer
  capabilities  the Effective Catalog's row counts
  commands      which project verbs are available here

It works before ` + "`init`" + ` and ` + "`setup`" + `: outside a Project Root it returns a valid
envelope with initialized false. It never prompts and never changes state.`,
		Example: `  igdev agent context
  igdev agent context --json`,
		Args: rejectArgs("agent context"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, res, err := a.gate()
			if err != nil {
				return err
			}
			// The Gate's verdict is reported, never enforced: context has to
			// work on the uninitialized and the stale checkout it is describing.
			state, _ := gate.Evaluate(a.gateInput(found))
			data := agentContextOf(found, res, state)
			a.emit(res, data, func() { a.printAgentContext(data) })
			return nil
		},
	}
}

// agentContextOf assembles the orientation data. Every part is best-effort: the
// call reports what it can see and never fails on the state it is describing.
func agentContextOf(found project.Found, res *config.Resolution, state gate.State) agentContextData {
	// A contract igdev cannot parse or does not speak is still worth orienting
	// on, so the document is decoded leniently here; the Gate's verdict, not
	// this decode, is what reports a broken contract.
	var doc project.Doc
	if len(found.ContractTOML) > 0 {
		if parsed, err := project.DecodeDoc(found.ContractTOML); err == nil {
			doc = parsed
		}
	}

	data := agentContextData{
		ProjectRoot: found.Root,
		Lifecycle: agentLifecycle{
			Initialized: found.InProject(),
			SetupState:  agentSetupState(found, state),
			Consent:     agentConsentState(),
		},
		Versions: agentVersions{
			CLI:         Version(),
			CLIContract: contract.Version,
			Ignition:    res.String("ignition.version"),
			Jython:      jythonVersion(doc),
		},
		Modules:  agentModulesOf(found, doc),
		Commands: agentCommandsOf(doc),
	}
	data.Catalog, data.Capabilities = agentCatalogOf(found, res, doc)

	if stamp, ok := gate.Decode(found.SetupRaw); ok && stamp.InstanceID != "" && stamp.Ports.Complete() {
		data.Instance = &agentInstance{
			ID:        stamp.InstanceID,
			Namespace: stamp.Namespace(),
			Ports:     stamp.Ports,
		}
		data.Gateway = &agentGateway{
			Running: agentGatewayRunning(found.Root, stamp),
			URL:     fmt.Sprintf("http://%s:%d", ports.BindAddress, stamp.Ports.HTTP),
		}
	}
	return data
}

// agentSetupState maps the Gate's verdict onto the frozen vocabulary. Outside a
// Project Root there is nothing to set up, so the state is `none` rather than
// the Gate's `required`.
func agentSetupState(found project.Found, state gate.State) string {
	if !found.InProject() {
		return setupStateNone
	}
	return string(state.Stamp)
}

// agentConsentState reads the machine-global Consent record. An unreadable
// record proves nothing, so every term reports unaccepted, exactly as status
// treats it.
func agentConsentState() agentConsent {
	record, _ := consent.Load(consent.Path(xdg.Resolve().Config))
	accepted := func(term consent.Term) bool {
		_, ok := record.Accepted(term)
		return ok
	}
	return agentConsent{
		IgnitionEULA:      accepted(consent.EULA),
		ModuleLicense:     accepted(consent.ModuleLicense),
		ModuleCertificate: accepted(consent.ModuleCert),
	}
}

// agentModulesOf reports the staged module artifacts, reusing the status report
// so both surfaces answer from the same read. An unreadable contract reports no
// staged modules and the option's default: the orientation never fails on the
// state it is describing.
func agentModulesOf(found project.Found, doc project.Doc) agentModules {
	staged := modulesStatus(found)
	autoAccepted := []string{}
	if !doc.Modules.RequirePrivateModuleConsent {
		autoAccepted = append(autoAccepted, staged.Staged...)
	}
	return agentModules{
		Count:                       staged.Count,
		Staged:                      staged.Staged,
		AllowUnsignedModules:        doc.Gateway.AllowUnsignedModules,
		AutoAccepted:                autoAccepted,
		RequirePrivateModuleConsent: doc.Modules.RequirePrivateModuleConsent,
	}
}

// agentCatalogOf resolves the Effective Catalog best-effort: an Invalid Project
// Overlay or an Ignition version this binary does not carry leaves the catalog
// block empty rather than failing the orientation. The digests, not the counts,
// are the layer identity (ADR 0005).
func agentCatalogOf(found project.Found, res *config.Resolution, doc project.Doc) (agentCatalog, agentCapabilities) {
	empty := agentCatalog{Overlay: agentCatalogLayer{Paths: []string{}}}
	overlay, fault := catalog.LoadOverlay(found.Root, doc.Catalog.OverlayPaths)
	if fault != nil {
		return empty, agentCapabilities{}
	}
	eff, fault := catalog.New(res.String("ignition.version"), overlay)
	if fault != nil {
		return empty, agentCapabilities{}
	}
	paths := eff.OverlayPaths()
	if paths == nil {
		paths = []string{}
	}
	overlayCounts := eff.OverlayCounts()
	effective := eff.Counts()
	return agentCatalog{
		CoreDigest: eff.CoreDigest(),
		Overlay: agentCatalogLayer{
			Present: len(paths) > 0,
			Digest:  eff.OverlayDigest(),
			Paths:   paths,
		},
	}, agentCapabilities{
		NativeFunctions: effective.NativeFunctions,
		RestOperations:  effective.RestOperations,
		OverlayRows: overlayCounts.BuiltinModules + overlayCounts.NativeFunctions +
			overlayCounts.CapabilityRules + overlayCounts.RestOperations,
	}
}

// agentCommandsOf reports which project verbs can do work here. The pipeline
// stages are declared in the Project Contract; the capability verbs are provided
// by this binary.
func agentCommandsOf(doc project.Doc) agentCommands {
	declared := func(stage string) bool { return strings.TrimSpace(stage) != "" }
	return agentCommands{
		Check:    declared(doc.Commands.Check),
		Test:     declared(doc.Commands.Test),
		Build:    declared(doc.Commands.Build),
		Verify:   true,
		Gateway:  true,
		Module:   true,
		Baseline: true,
		CILocal:  true,
	}
}

// agentGatewayRunning asks the container engine whether this Instance's Gateway
// is running. It reads only: no verb is ever started. A missing runtime file or
// an unreachable engine reports not-running rather than failing the orientation.
func agentGatewayRunning(root string, stamp gate.Stamp) bool {
	runtimeDir := filepath.Join(root, project.StateDir, "runtime")
	file := filepath.Join(runtimeDir, runtimeassets.ComposeFileName)
	if _, err := os.Stat(file); err != nil {
		return false
	}
	compose := docker.Compose{
		Namespace: stamp.Namespace(),
		File:      file,
		EnvFile:   filepath.Join(runtimeDir, runtimeassets.EnvFileName),
		Dir:       root,
	}
	services, fault := compose.Ps()
	if fault != nil {
		return false
	}
	for _, service := range services {
		if service.State == "running" {
			return true
		}
	}
	return false
}

// printAgentContext renders the orientation as prose for a human. Agents read the
// envelope; this is the convenience form, and it never prompts.
func (a *App) printAgentContext(data agentContextData) {
	if data.Lifecycle.Initialized {
		fmt.Fprintf(a.Stdout, "project:   %s\n", data.ProjectRoot)
	} else {
		fmt.Fprint(a.Stdout, "project:   not initialized\n")
	}
	fmt.Fprintf(a.Stdout, "setup:     %s\n", data.Lifecycle.SetupState)
	fmt.Fprintf(a.Stdout, "consent:   ignition-eula=%t module-license=%t module-certificate=%t\n",
		data.Lifecycle.Consent.IgnitionEULA, data.Lifecycle.Consent.ModuleLicense, data.Lifecycle.Consent.ModuleCertificate)
	fmt.Fprintf(a.Stdout, "cli:       %s (contract %s)\n", data.Versions.CLI, data.Versions.CLIContract)
	fmt.Fprintf(a.Stdout, "ignition:  %s\n", data.Versions.Ignition)
	fmt.Fprintf(a.Stdout, "jython:    %s\n", data.Versions.Jython)
	if data.Instance == nil {
		fmt.Fprint(a.Stdout, "instance:  none\n")
	} else {
		fmt.Fprintf(a.Stdout, "instance:  %s (%s)\n", data.Instance.ID, data.Instance.Namespace)
		fmt.Fprintf(a.Stdout, "ports:     http %d, https %d, debug %d\n",
			data.Instance.Ports.HTTP, data.Instance.Ports.HTTPS, data.Instance.Ports.Debug)
	}
	switch {
	case data.Gateway == nil:
		fmt.Fprint(a.Stdout, "gateway:   none\n")
	case data.Gateway.Running:
		fmt.Fprintf(a.Stdout, "gateway:   running %s\n", data.Gateway.URL)
	default:
		fmt.Fprintf(a.Stdout, "gateway:   not running %s\n", data.Gateway.URL)
	}
	if data.Modules.Count == 0 {
		fmt.Fprint(a.Stdout, "modules:   none staged\n")
	} else {
		fmt.Fprintf(a.Stdout, "modules:   %d staged (%s)\n", data.Modules.Count, strings.Join(data.Modules.Staged, ", "))
	}
	if data.Catalog.Overlay.Present {
		fmt.Fprintf(a.Stdout, "catalog:   core %s, overlay %s\n", data.Catalog.CoreDigest, data.Catalog.Overlay.Digest)
	} else {
		fmt.Fprintf(a.Stdout, "catalog:   core %s, overlay none\n", data.Catalog.CoreDigest)
	}
	fmt.Fprintf(a.Stdout, "commands:  check=%t test=%t build=%t verify=%t gateway=%t module=%t baseline=%t ci_local=%t\n",
		data.Commands.Check, data.Commands.Test, data.Commands.Build, data.Commands.Verify,
		data.Commands.Gateway, data.Commands.Module, data.Commands.Baseline, data.Commands.CILocal)
}

// agentSkillInstallData is the `data` member of a successful
// `igdev agent skill-install` envelope.
type agentSkillInstallData struct {
	// Scope is the resolved install scope: global or repo.
	Scope string `json:"scope"`
	// Path is the skill document's absolute path.
	Path string `json:"path"`
	// Action is created, updated, or unchanged.
	Action string `json:"action"`
	// Version is the CLI Contract Version the installed frontmatter carries.
	Version string `json:"version"`
}

func (a *App) newAgentSkillInstallCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "skill-install",
		Short: "Install the Agent Skill that matches this binary",
		Long: `skill-install writes the Agent Skill embedded in this binary. Its entry,
SKILL.md, is the thin workflow an agent follows when working an igdev repository.
Beside it, references/ holds the command reference and the error-code reference,
which an agent reads only when a task needs them. It is installed globally by
default, into ` + "`~/.agents/skills/igdev/`" + `, so one install covers every repository.
With ` + "`--scope repo`" + ` it goes into the repository instead, at
` + "`.agents/skills/igdev/`" + `, where it can be committed and reviewed.

The entry's frontmatter carries the CLI Contract Version this binary speaks, so the
guidance can never disagree with the tool. igdev owns SKILL.md and references/:
installation writes every embedded file and removes pages from references/ that the
embedded skill no longer carries. Other files beside them are left alone, and a
symlinked skill directory or references/ is written through, never replaced. It is
idempotent: files that already match are left untouched.`,
		Example: `  igdev agent skill-install
  igdev agent skill-install --scope repo
  igdev agent skill-install --json`,
		Args: rejectArgs("agent skill-install"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, res, err := a.gate()
			if err != nil {
				return err
			}
			data, fault := installAgentSkill(found, scope)
			if fault != nil {
				return fault
			}
			a.emit(res, data, func() { a.printAgentSkillInstall(data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", scopeGlobal,
		"where to install the skill: global (~/.agents/skills) or repo (.agents/skills)")
	return cmd
}

// installAgentSkill resolves the install directory for the scope and writes the
// embedded document there.
func installAgentSkill(found project.Found, scope string) (agentSkillInstallData, *contract.Fault) {
	var dir string
	switch strings.TrimSpace(scope) {
	case "", scopeGlobal:
		scope = scopeGlobal
		dir = filepath.Join(xdg.Home(), ".agents", "skills", agentskill.Dir)
	case scopeRepo:
		if !found.InProject() {
			return agentSkillInstallData{}, contract.NewFault(contract.CodeNotInitialized, contract.ExitFailure,
				fmt.Sprintf("--scope repo needs a Project Root: no %s was found at or above %s",
					project.ContractFile, found.StartDir)).
				WithRemediation(
					contract.Remediation{Command: "igdev init", Why: "create the Project Contract first"},
					contract.Remediation{Command: "igdev agent skill-install", Why: "install the skill globally instead"})
		}
		dir = filepath.Join(found.Root, ".agents", "skills", agentskill.Dir)
	default:
		return agentSkillInstallData{}, contract.UsageFault(
			fmt.Sprintf("--scope %q is not %s or %s", scope, scopeGlobal, scopeRepo),
			contract.Remediation{Command: "igdev help agent skill-install", Why: "show the accepted scopes"})
	}
	return writeAgentSkill(dir, scope)
}

// writeAgentSkill writes the embedded skill into dir and removes every page in
// references/ that the embedded skill does not carry, so pages an older binary
// wrote do not linger. igdev owns SKILL.md and references/; other files beside
// them are left alone. It reports created (no
// SKILL.md before), unchanged (every file already matched and nothing was
// removed), or updated. Identical files are left untouched, so a re-install
// neither writes nor moves their mtimes.
func writeAgentSkill(dir, scope string) (agentSkillInstallData, *contract.Fault) {
	entry := filepath.Join(dir, agentskill.FileName)
	action := "unchanged"
	if _, err := os.Lstat(entry); os.IsNotExist(err) {
		action = "created"
	} else if err != nil {
		return agentSkillInstallData{}, writeFault(entry, err)
	}

	embedded := agentskill.Files()
	for _, name := range sortedFileNames(embedded) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return agentSkillInstallData{}, writeFault(path, err)
		}
		if err == nil && bytes.Equal(existing, embedded[name]) {
			continue
		}
		if err := atomicfile.Write(path, embedded[name], 0o644, 0o755); err != nil {
			return agentSkillInstallData{}, writeFault(path, err)
		}
		if action == "unchanged" {
			action = "updated"
		}
	}

	removed, fault := pruneAgentSkill(dir, embedded)
	if fault != nil {
		return agentSkillInstallData{}, fault
	}
	if removed && action == "unchanged" {
		action = "updated"
	}
	return agentSkillInstallData{
		Scope:   scope,
		Path:    entry,
		Action:  action,
		Version: agentskill.Version(),
	}, nil
}

// pruneAgentSkill removes the files under dir's references/ that the embedded
// skill does not carry, then the directories that leaves empty. It reports
// whether it removed anything. Stale pages can only appear in references/, so
// nothing beside it is touched. The walk starts from references/ with its
// symlinks resolved, so a linked skill directory or a linked references/ is
// pruned through the link and the link itself is never removed.
func pruneAgentSkill(dir string, embedded map[string][]byte) (bool, *contract.Fault) {
	refs := filepath.Join(dir, agentskill.ReferencesDir)
	root, err := filepath.EvalSymlinks(refs)
	if os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, writeFault(refs, err)
	}
	var files, dirs []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := embedded[agentskill.ReferencesDir+"/"+filepath.ToSlash(rel)]; !ok {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return false, writeFault(refs, err)
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil {
			return false, writeFault(path, err)
		}
	}
	// WalkDir lists parents before children, so walking backwards removes a
	// child directory before its parent. A directory that still holds files
	// fails to remove and is kept; the root is never listed.
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			if err := os.Remove(dirs[i]); err != nil {
				return false, writeFault(dirs[i], err)
			}
			files = append(files, dirs[i])
		}
	}
	return len(files) > 0, nil
}

func sortedFileNames(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (a *App) printAgentSkillInstall(data agentSkillInstallData) {
	fmt.Fprintf(a.Stdout, "skill:     %s (%s, %s, contract %s)\n", data.Path, data.Action, data.Scope, data.Version)
}
