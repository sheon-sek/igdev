package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/buildinfo"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
)

// versionData is the `data` member of a successful `igdev version` envelope.
type versionData struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Contract string `json:"contract"`
}

func (a *App) newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version and the CLI Contract Version",
		Long: `version prints the release version of this binary and the CLI Contract Version it
speaks: the stability epoch of the JSON envelope, the IGDEV_E_* code namespace, and the
exit levels. The contract version changes only when the machine-facing interface breaks,
independently of the release semver.

igdev never updates itself (ADR 0002). In human mode this command may print a cached
"update available" notice on stderr; IGDEV_NO_UPDATE_NOTIFIER=1 suppresses it, and
--json suppresses all notices so machine output stays deterministic.`,
		Example: "  igdev version\n  igdev version --json",
		Args:    rejectArgs("version"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, res, err := a.gate()
			if err != nil {
				return err
			}
			data := versionData{
				Version:  buildinfo.Version,
				Commit:   buildinfo.Commit,
				Contract: contract.Version,
			}
			a.emit(res, data, func() { printVersion(a, res) })
			return nil
		},
	}
}

func printVersion(a *App, res *config.Resolution) {
	fmt.Fprintf(a.Stdout, "igdev %s\n", buildinfo.Version)
	fmt.Fprintf(a.Stdout, "cli contract %s\n", contract.Version)
	fmt.Fprintf(a.Stdout, "commit %s\n", buildinfo.Commit)
}
