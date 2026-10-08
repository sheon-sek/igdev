package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
)

// restartData is what `igdev restart` reports: where the Gateway is, the module
// state it came back with, and whether the upgrades that waited for the restart
// were finished.
type restartData struct {
	gatewayAddress
	Modules        gatewayModules `json:"modules"`
	PendingUpgrade pendingUpgrade `json:"pending_upgrade"`
}

// pendingUpgrade compares the modules that waited for a restart before it with
// the ones still waiting after it.
type pendingUpgrade struct {
	// Before lists the module ids installed over a running build and waiting for
	// a restart; After lists the ones still waiting once the Gateway runs again.
	Before []string `json:"before"`
	After  []string `json:"after"`
	// Finalized is true when nothing that waited before the restart waits after it.
	Finalized bool `json:"finalized"`
}

func (a *App) newRestartCmd() *cobra.Command {
	var timeoutRaw string
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the Gateway, wait for it, and report its modules",
		Long: `restart restarts this Instance's Gateway container in place and keeps everything
it holds: the named volume, the recorded ports, the projects and the configuration.
It then waits until the Gateway reports RUNNING, as ` + "`gateway wait`" + ` does, and
reports the Gateway's modules from its REST API: the healthy ones with their state,
and the quarantined ones with the reason the Gateway gives.

A module installed over a running build of the same id waits for a restart before it
replaces it (pending upgrade). The report lists those modules before and after the
restart; finalized is true when every one of them was upgraded.

A Gateway that comes back asking to accept a module's certificate or license (8.3
does so for an unsigned module installed over REST) has the modules step finished
for it when every module it asks about is one this checkout stages, as at start-up
(ADR 0006); any other commissioning step stops the run at exit level 3.

A quarantined module this checkout stages or installed fails the run with
IGDEV_E_MODULE_QUARANTINED and the Gateway's reason; an unsigned build names
` + "`allow_unsigned_modules`" + `. A quarantined module the checkout does not own is
reported, not failed.

` + "`gateway restart`" + ` is the bare container restart, without the wait or the report.`,
		Example: `  igdev restart
  igdev restart --timeout 5m --json`,
		Args: rejectArgs("restart"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			timeout, err := parseTimeout(timeoutRaw, gatewayWaitDefault)
			if err != nil {
				return err
			}
			data, err := a.restartGateway(g, timeout)
			if err != nil {
				return err
			}
			a.emit(g.res, data, func() { a.printRestart(data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait for RUNNING: seconds, or a duration like 3m (default 180s)")
	return cmd
}

// restartGateway restarts the container, waits for RUNNING, and reads the module
// state, failing when a module this checkout owns came back quarantined. It is
// `igdev restart`, and the step `module install` takes for a pending upgrade.
func (a *App) restartGateway(g *gateway, timeout time.Duration) (restartData, error) {
	staged, fault := stagedIDs(g)
	if fault != nil {
		return restartData{}, fault
	}
	before := []string{}
	// A Gateway that is down or still starting has no module state to compare;
	// the restart is still what the caller asked for.
	if m, fault := g.readModules(staged); fault == nil {
		before = m.pendingUpgrade()
	}
	a.stage("restart: restarting %s in place", g.stamp.Namespace())
	if fault := g.compose.Restart(); fault != nil {
		return restartData{}, fault
	}
	if err := a.waitForGateway(g, timeout); err != nil {
		return restartData{}, err
	}
	if err := a.finishModuleCommissioning(g, staged, timeout); err != nil {
		return restartData{}, err
	}
	after, fault := g.readModules(staged)
	if fault != nil {
		return restartData{}, fault
	}
	data := restartData{
		gatewayAddress: g.address(),
		Modules:        after,
		PendingUpgrade: pendingUpgrade{Before: before, After: after.pendingUpgrade()},
	}
	data.PendingUpgrade.Finalized = !overlaps(data.PendingUpgrade.Before, data.PendingUpgrade.After)
	if fault := quarantineFault(after, data); fault != nil {
		return data, fault
	}
	return data, nil
}

// stagedIDs lists the module ids this checkout stages, which are the modules it
// owns.
func stagedIDs(g *gateway) ([]string, *contract.Fault) {
	records, fault := modules.Scan(modules.Dir(g.found.Root))
	if fault != nil {
		return nil, fault
	}
	return modules.IDs(records), nil
}

// overlaps reports whether any id is in both lists.
func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// printRestart tells a human the Gateway is back and how its modules came back.
func (a *App) printRestart(data restartData) {
	fmt.Fprintf(a.Stdout, "restarted:   %s  %s\n", data.Namespace, data.URL)
	a.printModuleCounts(data.Modules)
	if len(data.PendingUpgrade.Before) > 0 {
		word := "finalized"
		if !data.PendingUpgrade.Finalized {
			word = "still pending: " + strings.Join(data.PendingUpgrade.After, ", ")
		}
		fmt.Fprintf(a.Stdout, "upgraded:    %s (%s)\n", strings.Join(data.PendingUpgrade.Before, ", "), word)
	}
}

// printModuleCounts summarizes the module state, naming every quarantined module.
func (a *App) printModuleCounts(m gatewayModules) {
	states := map[string]int{}
	var order []string
	for _, h := range m.Healthy {
		state := strings.ToLower(h.State)
		if states[state] == 0 {
			order = append(order, state)
		}
		states[state]++
	}
	parts := make([]string, 0, len(order)+1)
	for _, state := range order {
		parts = append(parts, fmt.Sprintf("%d %s", states[state], state))
	}
	parts = append(parts, fmt.Sprintf("%d quarantined", len(m.Quarantined)))
	fmt.Fprintf(a.Stdout, "modules:     %s\n", strings.Join(parts, ", "))
	for _, q := range m.Quarantined {
		fmt.Fprintf(a.Stdout, "quarantined: %s (%s)\n", q.ID, q.Reason)
	}
}
