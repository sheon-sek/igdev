package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
)

// consentExportData is what `igdev consent export --json` reports.
type consentExportData struct {
	// Source is the record that was exported.
	Source string `json:"source"`
	// Output is the file written, or "" when the record went to stdout.
	Output string `json:"output"`
	// Terms are the accepted term ids the export carries.
	Terms []string `json:"terms"`
	// Record is the exported file's content.
	Record string `json:"record"`
}

func (a *App) newConsentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consent",
		Short: "Carry a person's Consent record to an unattended runner",
		Long: `consent holds the one command that moves Consent between machines. Consent itself is
only ever recorded by a person, with ` + "`igdev setup --accept-eula`" + ` (ADR 0004).`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newConsentExportCmd())
	return cmd
}

func (a *App) newConsentExportCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export this machine's Consent record for IGDEV_CONSENT_FILE",
		Long: `export writes the Consent record a person recorded on this machine, for an unattended
runner that has no person: store the file as a CI secret and point IGDEV_CONSENT_FILE at
it there. With IGDEV_CONSENT_FILE set igdev reads the record from that file instead of
the machine-global one, refuses the --accept-* flags, and never writes a record
(ADR 0004, amendment 1).

The Ignition EULA must be accepted here first; the export carries every term this
machine accepted. --output writes the file with mode 0600; without it the record goes
to stdout.`,
		Example: `  igdev consent export --output igdev-consent.toml
  igdev consent export | gh secret set IGDEV_CONSENT`,
		Args: rejectArgs("consent export"),
		RunE: func(_ *cobra.Command, _ []string) error {
			_, res, err := a.gate()
			if err != nil {
				return err
			}
			location := a.consentLocation()
			raw, fault := consent.Export(location.Path, consent.EULA)
			if fault != nil {
				return fault
			}
			if output != "" {
				if err := atomicfile.Write(output, raw, 0o600, 0o700); err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot write %s: %v", output, err)).WithCause(err)
				}
			}
			record, _ := consent.Decode(raw)
			data := consentExportData{Source: location.Path, Output: output, Terms: record.IDs(), Record: string(raw)}
			a.emit(res, data, func() {
				if output == "" {
					_, _ = a.Stdout.Write(raw)
					return
				}
				fmt.Fprintf(a.Stdout, "consent exported: %s (%d terms); store it as a CI secret for %s\n",
					output, len(data.Terms), consent.FileEnv)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "write the record to this file (mode 0600) instead of stdout")
	return cmd
}
