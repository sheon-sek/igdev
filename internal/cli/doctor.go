package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/cleanup"
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
project's contract declares a Gradle command, and act is optional because only
` + "`igdev ci-local`" + ` needs it. data.ready is false when a required
prerequisite is not present.

doctor never fails: the exit level stays 0 and the audit is the payload, so an agent
reads the report instead of an error string. Probing touches nothing: no file is
written and no container is created, stopped or removed.

data.instances.items lists every igdev Instance the Docker engine holds containers for, with
the checkout its containers were created from and whether that path still exists:
active (the checkout's Setup records this Instance), needs_review (the path exists but
its Setup is missing or names another Instance), or checkout_missing. A missing path is
an observation, not proof the Instance is unwanted: a moved checkout keeps its old path
until its next up. Each container is running, restartable (the engine is restarting
it), or unknown (stopped; the engine does not say whether an operator stopped it). An
engine that cannot be asked is reported as unknown, never as zero Instances. A
non-active Instance carries a suggestion — docker compose -p <namespace> stop — that
stops only its containers without its Compose file and keeps its data volume; doctor
never runs it.`,
		Example: "  igdev doctor\n  igdev doctor --json",
		Args:    rejectArgs("doctor"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, res, err := a.gate()
			if err != nil {
				return err
			}
			report := doctor.Audit(doctor.System)
			report.Instances = cleanup.TakeInventory(cleanup.Exec)
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
		if entry.Warning != "" {
			fmt.Fprintf(a.Stdout, "  %-8s warning: %s\n", "", entry.Warning)
		}
	}
	a.printInventory(report.Instances)
}

// printInventory lists each Instance with its checkout and containers, and the
// suggestion for one that is not active.
func (a *App) printInventory(inv cleanup.Inventory) {
	if inv.Engine != cleanup.EngineAvailable {
		fmt.Fprintf(a.Stdout, "instances: unknown (%s)\n", inv.Error)
		return
	}
	fmt.Fprintf(a.Stdout, "instances: %d\n", len(inv.Instances))
	for _, in := range inv.Instances {
		fmt.Fprintf(a.Stdout, "  %s  %s  %s (%s)\n", in.Namespace, in.Classification, in.Checkout, in.PathState)
		for _, s := range in.Services {
			fmt.Fprintf(a.Stdout, "    %-12s %-10s restart: %s\n", s.Service, s.State, s.Restart)
		}
		if in.Suggestion != "" {
			fmt.Fprintf(a.Stdout, "    suggestion (not run): %s\n", in.Suggestion)
		}
	}
}

func readyWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "not ready (a required prerequisite is missing)"
}
