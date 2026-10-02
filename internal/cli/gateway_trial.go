package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/trial"
)

// trialRequestTimeout bounds one trial request. The reset answers once the Gateway
// has re-licensed itself, which takes longer than a health probe.
const trialRequestTimeout = 30 * time.Second

// gatewayTrialData is what `gateway trial` reports.
type gatewayTrialData struct {
	Instance string `json:"instance_id"`
	URL      string `json:"url"`
	// TrialReset is the effective [gateway] trial_reset: "auto" when the trial
	// keeper runs next to the Gateway, "off" when it does not.
	TrialReset string      `json:"trial_reset"`
	Trial      trial.State `json:"trial"`
}

// gatewayTrialResetData is what `gateway trial reset` reports: whether it reset,
// and the trial before and after.
type gatewayTrialResetData struct {
	Instance string       `json:"instance_id"`
	URL      string       `json:"url"`
	Reset    bool         `json:"reset"`
	Before   trial.State  `json:"before"`
	After    *trial.State `json:"after"`
}

func (a *App) newGatewayTrialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trial",
		Short: "Report this Instance's Gateway trial",
		Long: `trial reports the Gateway's license mode, how long its trial has left, and whether
it expired, as the Gateway itself reports them at GET /data/api/v1/trial, which needs
no credential.

An expired trial is reset in place, never by rebuilding or restarting the Gateway, so
its tags, history, connections and projects survive (ADR 0008). With the contract's
default [gateway] trial_reset = "auto" the trial keeper next to the Gateway does that
the moment the trial expires; ` + "`igdev gateway trial reset`" + ` does it on demand.`,
		Example: `  igdev gateway trial
  igdev gateway trial --json
  igdev gateway trial reset`,
		Args: rejectUnknownCommand,
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: trialRequestTimeout}
			state, err := trial.Read(client, g.url())
			if err != nil {
				return trialUnreadable(g, err, contract.CodeGatewayUnhealthy)
			}
			data := gatewayTrialData{
				Instance:   g.stamp.InstanceID,
				URL:        g.url(),
				TrialReset: g.doc.Gateway.EffectiveTrialReset(),
				Trial:      state,
			}
			a.emit(g.res, data, func() {
				fmt.Fprintf(a.Stdout, "trial:  %s\n", describeTrial(state))
				fmt.Fprintf(a.Stdout, "reset:  %s\n", data.TrialReset)
			})
			return nil
		},
	}
	cmd.AddCommand(a.newGatewayTrialResetCmd())
	return cmd
}

func (a *App) newGatewayTrialResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Reset this Instance's expired Gateway trial in place",
		Long: `reset starts a fresh trial on a Gateway whose trial expired, with the Instance's own
API token (ADR 0007) and without restarting or rebuilding anything, so nothing the
Gateway holds is lost (ADR 0008).

Ignition accepts the reset only once the trial expired. A trial that still has time
left is reported and left alone: that is a successful no-op, not a failure.`,
		Example: `  igdev gateway trial reset
  igdev gateway trial reset --json`,
		Args: rejectArgs("gateway trial reset"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: trialRequestTimeout}
			before, err := trial.Read(client, g.url())
			if err != nil {
				return trialUnreadable(g, err, contract.CodeTrialReset)
			}
			data := gatewayTrialResetData{Instance: g.stamp.InstanceID, URL: g.url(), Before: before}
			if !before.Expired {
				a.emit(g.res, data, func() {
					fmt.Fprintf(a.Stdout, "trial not expired (%s): nothing to reset\n", describeTrial(before))
				})
				return nil
			}
			if g.token == "" {
				return contract.NewFault(contract.CodeTrialReset, contract.ExitFailure,
					"the trial expired, and this checkout has no Instance API token to reset it with").
					WithRemediation(
						contract.Remediation{Command: "igdev setup", Why: "mint the Instance API token"},
						contract.Remediation{Command: "igdev gateway reset", Why: "rebuild the Gateway so it is seeded with the token (discards its data)"})
			}
			status, err := trial.Reset(client, g.url(), g.token)
			if err != nil {
				return trialUnreadable(g, err, contract.CodeTrialReset)
			}
			if fault := trialResetFault(g, status); fault != nil {
				return fault
			}
			data.Reset = true
			if after, err := trial.Read(client, g.url()); err == nil {
				data.After = &after
			}
			a.emit(g.res, data, func() {
				if data.After != nil {
					fmt.Fprintf(a.Stdout, "trial reset: %s\n", describeTrial(*data.After))
					return
				}
				fmt.Fprint(a.Stdout, "trial reset\n")
			})
			return nil
		},
	}
}

// trialResetFault names what a refused reset means. A 401 is a Gateway that does
// not know the token: its data volume predates the seed, and only a fresh volume
// takes one.
func trialResetFault(g *gateway, status int) *contract.Fault {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return contract.NewFault(contract.CodeTrialReset, contract.ExitFailure,
			fmt.Sprintf("the Gateway at %s does not know the Instance API token (HTTP 401): its data predates the token", g.url())).
			WithRemediation(contract.Remediation{
				Command: "igdev gateway reset",
				Why:     "rebuild the Gateway from a fresh volume, which is seeded with the token (discards its data)",
			})
	case status == http.StatusForbidden:
		return contract.NewFault(contract.CodeTrialReset, contract.ExitFailure,
			fmt.Sprintf("the Gateway at %s refused the reset (HTTP 403): the trial is no longer expired, or the token lost its rights", g.url())).
			WithRemediation(contract.Remediation{
				Command: "igdev gateway trial --json",
				Why:     "read the trial again; a reset is accepted only after it expired",
			})
	default:
		return contract.NewFault(contract.CodeTrialReset, contract.ExitFailure,
			fmt.Sprintf("the Gateway at %s answered the reset with HTTP %d", g.url(), status)).
			WithRemediation(contract.Remediation{
				Command: "igdev gateway logs --tail 200",
				Why:     "read the Gateway's own log",
			})
	}
}

// trialUnreadable is the fault for a Gateway that did not answer a trial request.
func trialUnreadable(g *gateway, err error, code contract.Code) *contract.Fault {
	return contract.NewFault(code, contract.ExitFailure,
		fmt.Sprintf("the Gateway at %s did not report its trial (%v)", g.url(), err)).
		WithCause(err).
		WithRemediation(
			contract.Remediation{Command: "igdev gateway status --json", Why: "check that the Gateway is running"},
			contract.Remediation{Command: "igdev gateway wait", Why: "wait until it reports RUNNING"})
}

// readTrial is the trial a running Gateway reports, or nil when it does not
// answer: status reports the trial, it never fails on it.
func readTrial(url string) *trial.State {
	state, err := trial.Read(&http.Client{Timeout: gatewayProbeTimeout}, url)
	if err != nil {
		return nil
	}
	return &state
}

// describeTrial is the one-line human form of a trial state.
func describeTrial(s trial.State) string {
	switch {
	case s.LicenseMode != "" && s.LicenseMode != "Trial":
		return s.LicenseMode
	case s.Expired:
		return "expired"
	default:
		left := time.Duration(s.SecondsLeft) * time.Second
		return fmt.Sprintf("%s left", left.Round(time.Minute))
	}
}
