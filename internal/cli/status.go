package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/project"
)

// statusData is the `data` member of a successful `igdev status` envelope. The
// key set and order are frozen by the goldens in itest/testdata/golden.
type statusData struct {
	Initialized bool             `json:"initialized"`
	ProjectRoot string           `json:"project_root"`
	WorkingDir  string           `json:"working_dir"`
	Contract    project.Contract `json:"contract"`
	Setup       statusSetup      `json:"setup"`
	Config      statusConfig     `json:"config"`
}

type statusSetup struct {
	Present bool   `json:"present"`
	Path    string `json:"path"`
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
working directory. Setup reports whether this checkout has a Checkout Setup record in
.igdev/setup.json; ticket 01 reports existence only, the Setup Stamp is validated later.

The config block shows every key igdev resolves, its value, and the tier that won:
flag > IGDEV_* environment > .igdev/local.toml > igdev.toml > embedded defaults.`,
		Example: "  igdev status --json\n  igdev status --config project.ignition_version=8.1.21",
		Args:    rejectArgs("status"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, res, err := a.gate()
			if err != nil {
				return err
			}
			data := statusData{
				Initialized: found.InProject(),
				ProjectRoot: found.Root,
				WorkingDir:  found.StartDir,
				Contract:    found.Contract,
				Setup: statusSetup{
					Present: found.Setup,
					Path:    found.SetupPath,
				},
				Config: statusConfig{
					TierFiles: res.TierFiles,
					Resolved:  res.Values(),
				},
			}
			a.emit(res, data, func() { a.printStatus(found, res) })
			return nil
		},
	}
}

func (a *App) printStatus(found project.Found, res *config.Resolution) {
	out := a.Stdout
	if found.InProject() {
		fmt.Fprint(out, "project:   initialized\n")
		fmt.Fprintf(out, "root:      %s\n", found.Root)
		fmt.Fprintf(out, "contract:  %s (schema %d)\n", found.Contract.Path, found.Contract.SchemaVersion)
	} else {
		fmt.Fprint(out, "project:   not initialized\n")
		fmt.Fprintf(out, "root:      none (no %s found at or above %s)\n", project.ContractFile, found.StartDir)
		fmt.Fprint(out, "contract:  missing\n")
	}
	if found.Setup {
		fmt.Fprintf(out, "setup:     %s\n", found.SetupPath)
	} else {
		fmt.Fprintf(out, "setup:     missing (%s)\n", found.SetupPath)
	}
	fmt.Fprint(out, "config:\n")
	table := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	for _, v := range res.Values() {
		fmt.Fprintf(table, "  %s\t%v\t[%s]\n", v.Key, v.Value, v.Source)
	}
	table.Flush()
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
