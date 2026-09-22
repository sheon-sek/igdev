package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
)

// statusData is the `data` member of a successful `igdev status` envelope. The
// key set and order are frozen by the goldens in itest/testdata/golden.
type statusData struct {
	Initialized bool           `json:"initialized"`
	ProjectRoot string         `json:"project_root"`
	WorkingDir  string         `json:"working_dir"`
	Contract    statusContract `json:"contract"`
	Setup       statusSetup    `json:"setup"`
	Config      statusConfig   `json:"config"`
}

// statusContract reports the Project Contract and the Gate's verdict on it.
type statusContract struct {
	Path string `json:"path"`
	// Present is whether a contract file was found at all.
	Present bool `json:"present"`
	// SchemaVersion is the version the file declares; 0 means it declares none.
	SchemaVersion int `json:"schema_version"`
	// SchemaSupported is whether this CLI speaks that schema version. An
	// unsupported contract fails every dependent command closed.
	SchemaSupported bool `json:"schema_supported"`
	// Digest is the Contract Digest: sha256 over the contract bytes.
	Digest string `json:"digest"`
}

// statusSetup reports the Checkout Setup and its Setup Stamp verdict.
type statusSetup struct {
	Present bool   `json:"present"`
	Path    string `json:"path"`
	// StampState is required (no record), current, or stale.
	StampState string `json:"stamp_state"`
}

type statusConfig struct {
	// TierFiles names the file tiers that actually exist on disk.
	TierFiles map[string]string `json:"tier_files"`
	// Resolved lists every config key with the tier that won precedence.
	Resolved []config.Value `json:"resolved"`
}

func (a *App) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report project, setup, and effective config state",
		Long: `status answers "where am I and what is configured" without changing anything.
It is the safe first call for a human or an agent, and it works before init and setup:
outside a Project Root it reports ok with initialized false.

Initialized means a Project Contract (igdev.toml) was found by searching upward from the
working directory. The contract block reports the declared schema version, whether this
CLI supports it (schema_supported), and the Contract Digest — sha256 over the contract
bytes.

setup reports the Checkout Setup record in .igdev/setup.json and its stamp_state:
required when the record is missing, current when its Setup Stamp matches the contract
digest, schema, and CLI Contract Version, and stale when any of them moved — including
after a hand-edit of the contract. A dependent command refuses to run on required or
stale; ` + "`igdev setup`" + ` re-materializes the checkout.

The config block shows every key igdev resolves, its value, and the tier that won:
flag > IGDEV_* environment > .igdev/local.toml > igdev.toml > embedded defaults.`,
		Example: "  igdev status --json\n  igdev status --config ignition.version=8.1.21",
		Args:    rejectArgs("status"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, res, err := a.gate()
			if err != nil {
				return err
			}
			// The Gate's verdict is reported, never enforced here: status has to
			// keep working on the checkout it is describing.
			state, _ := gate.Evaluate(a.gateInput(found))
			data := statusData{
				Initialized: found.InProject(),
				ProjectRoot: found.Root,
				WorkingDir:  found.StartDir,
				Contract: statusContract{
					Path:            found.Contract.Path,
					Present:         found.Contract.Present,
					SchemaVersion:   found.Contract.SchemaVersion,
					SchemaSupported: state.SchemaSupported,
					Digest:          state.Digest,
				},
				Setup: statusSetup{
					Present:    found.Setup,
					Path:       found.SetupPath,
					StampState: string(state.Stamp),
				},
				Config: statusConfig{
					TierFiles: res.TierFiles,
					Resolved:  res.Values(),
				},
			}
			a.emit(res, data, func() { a.printStatus(found, res, state) })
			return nil
		},
	}
}

func (a *App) printStatus(found project.Found, res *config.Resolution, state gate.State) {
	out := a.Stdout
	if found.InProject() {
		fmt.Fprint(out, "project:   initialized\n")
		fmt.Fprintf(out, "root:      %s\n", found.Root)
		fmt.Fprintf(out, "contract:  %s (schema %d)\n", found.Contract.Path, found.Contract.SchemaVersion)
		fmt.Fprintf(out, "digest:    %s\n", state.Digest)
		fmt.Fprintf(out, "schema:    %s\n", supportedWord(state.SchemaSupported))
		if found.Setup {
			fmt.Fprintf(out, "setup:     %s\n", found.SetupPath)
		} else {
			fmt.Fprintf(out, "setup:     missing (%s)\n", found.SetupPath)
		}
		fmt.Fprintf(out, "stamp:     %s\n", state.Stamp)
	} else {
		fmt.Fprint(out, "project:   not initialized\n")
		fmt.Fprintf(out, "root:      none (no %s found at or above %s)\n", project.ContractFile, found.StartDir)
		fmt.Fprint(out, "contract:  missing\n")
		fmt.Fprintf(out, "setup:     missing (%s)\n", found.SetupPath)
	}
	fmt.Fprint(out, "config:\n")
	table := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	for _, v := range res.Values() {
		fmt.Fprintf(table, "  %s\t%v\t[%s]\n", v.Key, v.Value, v.Source)
	}
	table.Flush()
}

func supportedWord(supported bool) string {
	if supported {
		return "supported"
	}
	return "unsupported"
}

// rejectArgs freezes "this command takes no positional arguments".
func rejectArgs(name string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return extraArguments(name, 0, args)
		}
		return nil
	}
}
