package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/doctor"
)

func (a *App) newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Audit the host prerequisites igdev's runtime work depends on",
		Long: `doctor audits this machine, read-only: it probes the commands igdev needs to run a
Gateway and to check Jython code, and reports each one with the version line it printed
or the error that says why it could not be used. It works before init and setup and
outside a Project Root — it is about the host, not the checkout.

docker and its Compose plugin run the Gateway, and a JVM runs the Jython compatibility
check, so those are required; Gradle is optional because it is only needed when a
project's contract declares a Gradle command. data.ready is false when a required
prerequisite is not present.

doctor never fails: the exit level stays 0 and the audit is the payload, so an agent
reads the report instead of an error string. Probing touches nothing: no file is
written and no container is created.`,
		Example: "  igdev doctor\n  igdev doctor --json",
		Args:    rejectArgs("doctor"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, res, err := a.gate()
			if err != nil {
				return err
			}
			report := doctor.Audit(doctor.System)
			a.emit(res, report, func() { a.printDoctor(report) })
			return nil
		},
	}
}

// printDoctor lists the audit as one line per prerequisite. A missing tool's own
// error is printed verbatim: it is the evidence the reader needs to fix the host.
func (a *App) printDoctor(report doctor.Report) {
	fmt.Fprintf(a.Stdout, "host:  %s\n", readyWord(report.Ready))
	for _, entry := range report.Prerequisites {
		requirement := "optional"
		if entry.Required {
			requirement = "required"
		}
		detail := entry.Version
		if detail == "" {
			detail = entry.Error
		}
		fmt.Fprintf(a.Stdout, "  %-8s %-8s %-8s %s\n", entry.Name, requirement, entry.State, detail)
	}
}

func readyWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "not ready (a required prerequisite is missing)"
}
