package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/instance"
	"github.com/sheon-sek/igdev/internal/localconfig"
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
}

func (a *App) newSetupCmd() *cobra.Command {
	var (
		acceptEULA          bool
		acceptModuleLicense bool
		acceptModuleCert    bool
		adminUsername       string
		adminPassword       string
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
free memory.`,
		Example: `  igdev setup
  igdev setup --accept-eula
  igdev setup --json
  IGDEV_GATEWAY_ADMIN_PASSWORD=... igdev setup`,
		Args: rejectArgs("setup"),
		RunE: func(_ *cobra.Command, _ []string) error {
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

			consentPath := consent.Path(xdg.Resolve().Config)
			now := time.Now().UTC()
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
				changed, err := consent.Accept(consentPath, term.term, now, Version())
				if err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot record %s consent in %s: %v", term.term.Title, consentPath, err)).
						WithCause(err)
				}
				if changed {
					accepted = append(accepted, term.term.ID)
				}
			}
			// Nothing is materialized until the required terms are on record: a
			// missing term is a human action, not a transient failure.
			if fault := consent.Check(consentPath, consent.EULA); fault != nil {
				return fault
			}

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
			if !triplet.Free() {
				// Bind probe, never a formula: a port another Instance took since the
				// last setup is re-allocated rather than recorded into a collision.
				triplet, fault = ports.Allocate()
				if fault != nil {
					return fault
				}
			}
			createdAt := previous.CreatedAt
			if createdAt == "" {
				createdAt = now.Format(time.RFC3339)
			}

			stateDir := filepath.Join(found.Root, project.StateDir)
			runtimeDir := filepath.Join(stateDir, "runtime")
			modulesDir := filepath.Join(stateDir, "modules")
			restoreDir := filepath.Join(stateDir, "restore")

			rendered := runtimeassets.Input{
				InstanceID:      instanceID,
				Namespace:       instance.Namespace(instanceID),
				IgnitionVersion: doc.Ignition.Version,
				JythonVersion:   doc.Ignition.JythonVersion,
				Edition:         doc.Ignition.Edition,
				Modules:         doc.Modules.Enabled,
				MemoryMB:        doc.Gateway.MemoryMB,
				Timezone:        doc.Gateway.Timezone,
				Ports:           triplet,
				RuntimeDir:      runtimeDir,
				ModulesDir:      modulesDir,
				RestoreDir:      restoreDir,
			}
			files, err := runtimeassets.Materialize(rendered)
			if err != nil {
				return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
					fmt.Sprintf("cannot render the runtime files: %v", err)).WithCause(err)
			}

			writes := make([]setupWrite, 0, 8)
			for _, dir := range []string{runtimeDir, modulesDir, restoreDir} {
				write, err := materializeDir(dir)
				if err != nil {
					return err
				}
				writes = append(writes, write)
			}
			for _, file := range files {
				write, err := materializeFile(filepath.Join(runtimeDir, file.Name), file.Data, renderedMode)
				if err != nil {
					return err
				}
				writes = append(writes, write)
			}

			creds, source, err := a.adminCredentials(found, adminUsername, adminPassword)
			if err != nil {
				return err
			}
			localPath := filepath.Join(stateDir, project.LocalConfig)
			changed, err := localconfig.Write(localPath, creds)
			if err != nil {
				return writeFault(localPath, err)
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
			}
			a.emit(res, data, func() { a.printSetup(data) })
			return nil
		},
	}

	flags := cmd.Flags()
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

	return cmd
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
