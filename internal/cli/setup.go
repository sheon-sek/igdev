package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/baseline"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/instance"
	"github.com/sheon-sek/igdev/internal/localconfig"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/runtimeassets"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The permissions of the materialized Checkout Setup. Rendered runtime files are
// ordinary state; the files carrying identity and credentials are private.
const (
	// stateDirMode is used for .igdev/ and its subdirectories.
	stateDirMode = 0o755
	// renderedMode is used for the Compose file, the Compose environment, and the
	// Dockerfile: they hold no secret.
	renderedMode = 0o644
	// privateMode is used for setup.json and local.toml.
	privateMode = 0o600
)

// setupWrite is one path the materialization touched, as reported to the caller.
type setupWrite struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`   // dir | file
	Action string `json:"action"` // created | updated | unchanged
	Mode   string `json:"mode"`
}

// setupCredentials reports where the admin password came from. It deliberately
// carries no password: `igdev gateway credentials --json` (ticket 11) is the only
// way to read one back out.
type setupCredentials struct {
	Path string `json:"path"`
	// Source is generated, flag, environment, or existing.
	Source   string `json:"source"`
	Username string `json:"username"`
}

// setupData is the `data` member of a successful `igdev setup` envelope. The key
// set and order are frozen by the goldens in itest/testdata/golden.
type setupData struct {
	Instance  string        `json:"instance_id"`
	Namespace string        `json:"namespace"`
	Ports     ports.Triplet `json:"ports"`
	SetupPath string        `json:"setup_path"`
	// ConsentAccepted lists the terms this run recorded on the human's behalf,
	// in the frozen term order. Empty when the machine was already consented.
	ConsentAccepted []string         `json:"consent_accepted"`
	Credentials     setupCredentials `json:"credentials"`
	Files           []setupWrite     `json:"files"`
	// Baseline is the Baseline this run staged, and is absent unless the run was
	// asked for one: every other setup envelope keeps its frozen key set.
	Baseline *baselineData `json:"baseline,omitempty"`
}

func (a *App) newSetupCmd() *cobra.Command {
	var (
		wizard              wizardFlags
		acceptEULA          bool
		acceptModuleLicense bool
		acceptModuleCert    bool
		adminUsername       string
		adminPassword       string
		gatewayPort         int
		baselinePath        string
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Materialize this checkout: Instance identity, ports, runtime files",
		Long: `setup materializes the Checkout Setup: the disposable, gitignored .igdev/ state
this checkout runs from. It writes the Setup Stamp (Contract Digest, schema, CLI
Contract Version), mints the Instance identity, allocates the loopback ports by bind
probe, renders the Compose file, Compose environment, and Dockerfile from the
templates embedded in the binary, and generates the Gateway admin password into a
0600 local config. It mutates nothing that git tracks.

The Instance identity is a random UUID minted here and never derived from the checkout
path, so two worktrees of one repository are two Instances with disjoint ports and
their own Docker namespace (igdev-<first 8 hex of the UUID>). Re-running setup on a
current checkout re-materializes it: the instance_id, its ports, and its creation time
are kept, so identical state produces byte-identical files. When the Contract Digest
moved, setup refreshes the Setup Stamp to the current contract. A recorded port the
machine no longer offers is re-allocated and the record updated; ports never appear in
the tracked Project Contract (ADR 0003).

Consent is required before anything is written. The record is machine-global
(~/.config/igdev/accepted.toml, ADR 0004) and only a human-invoked command writes it,
so an automated run that finds a term missing stops with IGDEV_E_CONSENT_REQUIRED at
exit level 3 and names the exact command below. Passing --accept-eula records the
Ignition EULA acceptance for this machine; re-running it once accepted is a no-op.

The admin password comes from --admin-password, then IGDEV_GATEWAY_ADMIN_PASSWORD, then
the local config that is already there, and is generated otherwise. It is never printed
in either dialect: read it with ` + "`igdev gateway credentials --json`" + `.

The Capacity Gate (ADR 0003) attaches where a Gateway starts, not here: setup records
the requested heap, and ` + "`igdev gateway up`" + ` (ticket 10) compares it against the host's
free memory.

A terminal gets the setup Wizard when the checkout has not been materialized yet, when
no admin password is on record, or when this machine has not accepted the Ignition
EULA. Its steps are the Consent gate, the requested heap, the admin password, an
optional Baseline, an optional port pin, the materialization, and the summary.
--interactive runs it even when everything is already on record; --yes takes the same
defaults without asking; --json never prompts, whatever the terminal is. The Consent
step only shows ` + "`igdev setup --accept-eula`" + `: a Wizard answer is never an acceptance,
and the run stops at exit level 3 until the machine record itself says otherwise.

--gateway-port pins the Instance's HTTP port machine-locally: the pin is recorded in
` + "`.igdev/local.toml`" + ` and re-used by the next setup, and the tracked Project Contract
never carries a port (ADR 0003). --baseline stages a ` + ".gwbk" + ` as the Baseline the
next fresh launch restores from, which is what ` + "`igdev gateway reset`" + ` applies.`,
		Example: `  igdev setup
  igdev setup --accept-eula
  igdev setup --json
  IGDEV_GATEWAY_ADMIN_PASSWORD=... igdev setup`,
		Args: rejectArgs("setup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, err := project.Discover(a.Dir)
			if err != nil {
				return err
			}
			res, err := a.resolvedForSetup(found)
			if err != nil {
				return err
			}
			// setup is a Gate repair path: it runs whatever the contract says, so a
			// missing or stale Checkout Setup is exactly what it is here to fix.
			// Everything else the Gate refuses — no contract, an unsupported schema,
			// a contract requiring a newer igdev — still stops it.
			state, fault := gate.Evaluate(a.gateInput(found))
			if fault != nil && !repairableBySetup(fault) {
				return fault
			}
			doc, err := project.ParseDoc(found.ContractTOML, found.Contract.Path)
			if err != nil {
				return err
			}
			// A contract that leaves a field out resolves to the schema default, so
			// the rendered runtime describes the same environment every other command
			// resolves.
			doc = doc.Filled(project.DefaultDoc())

			// The interaction flags are settled before anything is written: a
			// usage error must not leave a Consent record behind, and the one
			// decision is what lets the Acceptance flags below run before the
			// Wizard reads the Consent record.
			run, err := a.decide("setup", wizard, res.IsJSON(), a.setupNeedsWizard(found))
			if err != nil {
				return err
			}

			// The Acceptance flags are consumed here, before the Wizard whose
			// step 1 is the Consent gate: that step reads the record these flags
			// write, so consuming them afterwards dead-ends the run in the gate
			// that names the flag. Only a human-typed flag reaches this loop
			// (ADR 0004). acceptedAt is the acceptance, not the materialization:
			// the timestamps below are taken after a possibly long walkthrough.
			consentPath := consent.Path(xdg.Resolve().Config)
			acceptedAt := time.Now().UTC()
			accepted := []string{}
			for _, term := range []struct {
				term   consent.Term
				accept bool
			}{
				{consent.EULA, acceptEULA},
				{consent.ModuleLicense, acceptModuleLicense},
				{consent.ModuleCert, acceptModuleCert},
			} {
				if !term.accept {
					continue
				}
				changed, err := consent.Accept(consentPath, term.term, acceptedAt, Version())
				if err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot record %s consent in %s: %v", term.term.Title, consentPath, err)).
						WithCause(err)
				}
				if changed {
					accepted = append(accepted, term.term.ID)
				}
			}

			// The Wizard is a value source above the materialization: its answers
			// arrive as flag values, and everything below runs once, unchanged.
			wizardState, err := a.runSetupWizard(cmd, found, run, doc)
			if err != nil {
				return err
			}

			// Nothing is materialized until the required terms are on record: a
			// missing term is a human action, not a transient failure.
			if fault := consent.Check(consentPath, consent.EULA); fault != nil {
				return fault
			}

			// The materialization's timestamp is taken here, after the Wizard: a
			// walkthrough can take minutes, and the files it stamps are written
			// when setup materializes, not when it was invoked.
			now := time.Now().UTC()

			// The previous record is the source of continuity: the Instance keeps its
			// identity, its ports while they are still free, and its creation time.
			previous, _ := gate.Decode(found.SetupRaw)
			instanceID := previous.InstanceID
			if !instance.Valid(instanceID) {
				minted, err := instance.NewID()
				if err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot mint the Instance identity: %v", err)).WithCause(err)
				}
				instanceID = minted
			}
			triplet := previous.Ports
			pin, fault := a.gatewayPortPin(found, gatewayPort)
			if fault != nil {
				return fault
			}
			switch {
			case pin > 0 && triplet.HTTP != pin:
				// The pin moved: the triplet is re-allocated around it.
				triplet, fault = ports.AllocateFrom(pin)
			case triplet.Free():
				// The recorded triplet still binds, so the Instance keeps it.
			case pin > 0:
				triplet, fault = ports.AllocateFrom(pin)
			default:
				// Bind probe, never a formula: a port another Instance took since the
				// last setup is re-allocated rather than recorded into a collision.
				triplet, fault = ports.Allocate()
			}
			if fault != nil {
				return fault
			}
			createdAt := previous.CreatedAt
			if createdAt == "" {
				createdAt = now.Format(time.RFC3339)
			}

			writes, err := a.materializeRuntime(found, doc, instanceID, triplet)
			if err != nil {
				return err
			}

			// A Baseline named on the command line is staged before the Setup
			// Stamp is written: a run that dies part way leaves the old stamp, so
			// the checkout reads as stale and setup runs again.
			var staged *baselineData
			if baselinePath != "" {
				baselineState, fault := baseline.Stage(baselinePath, baseline.Dir(filepath.Join(found.Root, project.StateDir)), now)
				if fault != nil {
					return fault
				}
				rendered := baselineOf(baselineState)
				staged = &rendered
			}

			stateDir := filepath.Join(found.Root, project.StateDir)
			creds, source, err := a.adminCredentials(found, adminUsername, adminPassword)
			if err != nil {
				return err
			}
			localPath := filepath.Join(stateDir, project.LocalConfig)
			changed, err := localconfig.Write(localPath, creds)
			if err != nil {
				return writeFault(localPath, err)
			}
			// A pin is machine-local state, so it is recorded in the
			// checkout-local tier and never in the tracked contract (ADR 0003).
			if pin > 0 {
				pinned, err := localconfig.WritePort(localPath, pin)
				if err != nil {
					return writeFault(localPath, err)
				}
				changed = changed || pinned
			}
			writes = append(writes, setupWrite{
				Path:   localPath,
				Kind:   "file",
				Action: actionOf(changed, found.LocalConfigPath != ""),
				Mode:   fmt.Sprintf("%04o", localconfig.Mode),
			})

			// The record is written last: a run that dies part way leaves the old
			// Setup Stamp, so the checkout reads as stale and setup runs again.
			stamp := gate.Stamp{
				Schema:         gate.StampSchema,
				InstanceID:     instanceID,
				ContractDigest: state.Digest,
				ContractSchema: state.Schema,
				CLIContract:    contract.Version,
				Ports:          triplet,
				CreatedAt:      createdAt,
			}
			raw, err := stamp.Encode()
			if err != nil {
				return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
					fmt.Sprintf("cannot encode the Setup Stamp: %v", err)).WithCause(err)
			}
			write, err := materializeFile(found.SetupPath, raw, privateMode)
			if err != nil {
				return err
			}
			writes = append(writes, write)

			data := setupData{
				Instance:        instanceID,
				Namespace:       instance.Namespace(instanceID),
				Ports:           triplet,
				SetupPath:       found.SetupPath,
				ConsentAccepted: accepted,
				Credentials: setupCredentials{
					Path:     localPath,
					Source:   source,
					Username: creds.Username,
				},
				Files: writes,
				// Baseline is what --baseline staged, and is absent when the run
				// staged none, so every other run's envelope is unchanged.
				Baseline: staged,
			}
			a.emit(res, data, func() { a.printSetup(data) })
			a.summarizeSetupWizard(wizardState, data)
			return nil
		},
	}

	flags := cmd.Flags()
	wizard.register(cmd)
	flags.BoolVar(&acceptEULA, "accept-eula", false,
		"record the Ignition EULA acceptance for this machine (human-only, ADR 0004)")
	flags.BoolVar(&acceptModuleLicense, "accept-module-license", false,
		"record the module-license acceptance for this machine (human-only, ADR 0004)")
	flags.BoolVar(&acceptModuleCert, "accept-module-certificate", false,
		"record the module-certificate acceptance for this machine (human-only, ADR 0004)")
	flags.StringVar(&adminUsername, "admin-username", "",
		"Gateway admin username (default "+localconfig.DefaultUsername+")")
	flags.StringVar(&adminPassword, "admin-password", "",
		"Gateway admin password (default: "+localconfig.EnvPassword+" or a generated one; the value is never printed)")
	flags.IntVar(&gatewayPort, "gateway-port", 0,
		"pin the Gateway HTTP port for this checkout, recorded machine-locally in "+project.LocalConfig)
	flags.StringVar(&baselinePath, "baseline", "",
		"stage a .gwbk as this checkout's Baseline, restored by `igdev gateway reset`")

	return cmd
}

// gatewayPortPin decides the machine-local Gateway HTTP port pin: the flag wins
// over the environment, which wins over the pin this checkout already recorded.
// A pin is a property of the machine, so the tracked Project Contract never
// carries one (ADR 0003) and neither does any rendered output.
func (a *App) gatewayPortPin(found project.Found, flagValue int) (int, *contract.Fault) {
	if flagValue != 0 {
		if flagValue < 1 || flagValue > 65535 {
			return 0, contract.UsageFault(
				fmt.Sprintf("--gateway-port %d is not a port between 1 and 65535", flagValue),
				contract.Remediation{Command: "igdev setup --gateway-port 8088", Why: "pin a usable loopback port"})
		}
		return flagValue, nil
	}
	if raw := config.EnvironMap(a.Environ)[localconfig.EnvPort]; raw != "" {
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || port < 1 || port > 65535 {
			return 0, configValueFault(localconfig.EnvPort, raw, "a port between 1 and 65535")
		}
		return port, nil
	}
	port, err := localconfig.LoadPort(found.LocalConfigTOML)
	if err != nil {
		return 0, configValueFault(found.LocalConfigPath, err.Error(), "a port between 1 and 65535")
	}
	return port, nil
}

// configValueFault is a config tier that exists but cannot be interpreted, which
// is IGDEV_E_CONFIG_INVALID rather than a usage error: the invocation did not
// type it.
func configValueFault(where, got, want string) *contract.Fault {
	return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
		fmt.Sprintf("%s carries %s, which is not %s", where, got, want)).
		WithRemediation(contract.Remediation{
			Command: "igdev setup --gateway-port 8088",
			Why:     "pin the port explicitly for one run",
		})
}

// adminCredentials decides the Gateway admin credentials: the flag wins over the
// environment, which wins over what the checkout already recorded, which wins
// over a freshly generated password. A password on disk is never regenerated, so
// re-running setup does not rotate a credential the Gateway is already using.
func (a *App) adminCredentials(found project.Found, flagUsername, flagPassword string) (localconfig.Credentials, string, error) {
	existing, _ := localconfig.Load(found.LocalConfigTOML)
	env := config.EnvironMap(a.Environ)

	username := firstNonEmpty(flagUsername, env[localconfig.EnvUsername], existing.Username, localconfig.DefaultUsername)
	switch {
	case flagPassword != "":
		return localconfig.Credentials{Username: username, Password: flagPassword}, "flag", nil
	case env[localconfig.EnvPassword] != "":
		return localconfig.Credentials{Username: username, Password: env[localconfig.EnvPassword]}, "environment", nil
	case existing.Password != "":
		return localconfig.Credentials{Username: username, Password: existing.Password}, "existing", nil
	}
	password, err := localconfig.GeneratePassword()
	if err != nil {
		return localconfig.Credentials{}, "", contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot generate the Gateway admin password: %v", err)).WithCause(err)
	}
	return localconfig.Credentials{Username: username, Password: password}, "generated", nil
}

// repairableBySetup reports the two Gate faults setup exists to clear: no
// Checkout Setup at all, and one that no longer matches the contract.
func repairableBySetup(fault *contract.Fault) bool {
	return fault.Code == contract.CodeSetupRequired || fault.Code == contract.CodeSetupStale
}

// actionOf names what a write did. existed is whether the path was there before.
func actionOf(changed, existed bool) string {
	switch {
	case changed && existed:
		return "updated"
	case changed:
		return "created"
	default:
		return "unchanged"
	}
}

// runtimePaths are the Checkout Setup directories a materialization owns: the
// build context, the module staging directory the Gateway mounts, and the
// Baseline restore mount point.
type runtimePaths struct {
	state    string
	runtime  string
	modules  string
	baseline string
}

// setupPaths resolves the Checkout Setup directories of one Project Root.
func setupPaths(root string) runtimePaths {
	state := filepath.Join(root, project.StateDir)
	return runtimePaths{
		state:    state,
		runtime:  filepath.Join(state, "runtime"),
		modules:  modules.Dir(root),
		baseline: baseline.Dir(state),
	}
}

// materializeRuntime creates the Instance's runtime directories and renders its
// runtime files, reporting what each write did.
//
// Everything staged in the module directory is mounted by the rendered Compose
// file, so a materialization is also what makes a staging change visible to the
// next `gateway up`. `setup` runs it for a fresh or stale checkout; the module
// write verbs run it again after changing what is staged, which is why an
// artifact added to a current checkout reaches the Gateway without the contract —
// and therefore the Setup Stamp — moving.
//
// The Baseline directory is only created: what is staged inside it is user state
// that outlives a re-materialization.
func (a *App) materializeRuntime(found project.Found, doc project.Doc, instanceID string, triplet ports.Triplet) ([]setupWrite, error) {
	paths := setupPaths(found.Root)
	rendered := runtimeassets.Input{
		InstanceID:           instanceID,
		Namespace:            instance.Namespace(instanceID),
		IgnitionVersion:      doc.Ignition.Version,
		JythonVersion:        doc.Ignition.JythonVersion,
		Edition:              doc.Ignition.Edition,
		Modules:              doc.Modules.Enabled,
		MemoryMB:             doc.Gateway.MemoryMB,
		Timezone:             doc.Gateway.Timezone,
		AllowUnsignedModules: doc.Gateway.AllowUnsignedModules,
		Ports:                triplet,
		RuntimeDir:           paths.runtime,
		ModulesDir:           paths.modules,
		BaselineDir:          paths.baseline,
	}
	files, err := runtimeassets.Materialize(rendered)
	if err != nil {
		return nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot render the runtime files: %v", err)).WithCause(err)
	}

	writes := make([]setupWrite, 0, len(files)+3)
	for _, dir := range []string{paths.runtime, paths.modules, paths.baseline} {
		write, err := materializeDir(dir)
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}
	for _, file := range files {
		write, err := materializeFile(filepath.Join(paths.runtime, file.Name), file.Data, renderedMode)
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}
	return writes, nil
}

// materializeFile writes one generated file, reporting what happened. Generated
// state is compared byte for byte, so a re-setup that changes nothing writes
// nothing and leaves the tree alone.
func materializeFile(path string, data []byte, mode os.FileMode) (setupWrite, error) {
	out := setupWrite{Path: path, Kind: "file", Mode: fmt.Sprintf("%04o", mode)}
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		out.Action = "created"
	case err != nil:
		return setupWrite{}, writeFault(path, err)
	case bytes.Equal(existing, data):
		out.Action = "unchanged"
		return out, nil
	default:
		out.Action = "updated"
	}
	if err := atomicfile.Write(path, data, mode, stateDirMode); err != nil {
		return setupWrite{}, writeFault(path, err)
	}
	return out, nil
}

// materializeDir creates one directory of the Checkout Setup, reporting what
// happened.
func materializeDir(path string) (setupWrite, error) {
	out := setupWrite{Path: path, Kind: "dir", Mode: fmt.Sprintf("%04o", stateDirMode)}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		out.Action = "unchanged"
		return out, nil
	}
	if err := os.MkdirAll(path, stateDirMode); err != nil {
		return setupWrite{}, writeFault(path, err)
	}
	out.Action = "created"
	return out, nil
}

// printSetup tells a human what the checkout now holds. The password is
// deliberately absent: it exists in one 0600 file, and printing it anywhere else
// would put a credential in a terminal's scrollback.
func (a *App) printSetup(data setupData) {
	fmt.Fprintf(a.Stdout, "instance:    %s (%s)\n", data.Instance, data.Namespace)
	fmt.Fprintf(a.Stdout, "ports:       http %d, https %d, debug %d\n",
		data.Ports.HTTP, data.Ports.HTTPS, data.Ports.Debug)
	fmt.Fprintf(a.Stdout, "credentials: %s (%s, user %s, never printed)\n",
		data.Credentials.Path, data.Credentials.Source, data.Credentials.Username)
	if len(data.ConsentAccepted) > 0 {
		fmt.Fprintf(a.Stdout, "consent:     accepted %v\n", data.ConsentAccepted)
	} else {
		fmt.Fprint(a.Stdout, "consent:     already accepted\n")
	}
	fmt.Fprint(a.Stdout, "materialized:\n")
	for _, write := range data.Files {
		fmt.Fprintf(a.Stdout, "  %-4s %-9s %s  %s\n", write.Kind, write.Action, write.Mode, write.Path)
	}
}

// firstNonEmpty returns the first value that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
