package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/apitoken"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/runtimeassets"
	"github.com/sheon-sek/igdev/internal/trial"
)

// The actions `gateway ensure` reports.
const (
	ensureReused  = "reused"
	ensureStarted = "started"
	ensureReset   = "reset"
)

// The reasons `gateway ensure` reports for the action it took.
const (
	ensureHealthy       = "healthy"
	ensureNotRunning    = "not_running"
	ensureFresh         = "fresh"
	ensureFaulted       = "faulted"
	ensureTokenRejected = "token_rejected"
	ensureTrialShort    = "trial_short"
)

// faultedState is the state a Gateway that failed to start reports on /StatusPing.
const faultedState = "FAULTED"

// tokenProbePath is an endpoint that answers 200 to a known API token and 401 to
// anything else, so it tells a Gateway that holds the Instance token apart from
// one whose volume predates it.
const tokenProbePath = "/data/api/v1/gateway-info"

// gatewayEnsureData is what `gateway ensure` reports: what it did, why, and where
// the Gateway now is.
type gatewayEnsureData struct {
	gatewayAddress
	// Action is reused, started, or reset.
	Action string `json:"action"`
	// Reason is why: healthy, not_running, fresh, faulted, token_rejected, or
	// trial_short.
	Reason      string `json:"reason"`
	Container   string `json:"container"`
	HostAddress string `json:"host_address"`
	// Trial is what the Gateway reports about its trial, or null when it did not
	// answer.
	Trial *trial.State `json:"trial"`
	// Capacity is the Capacity Gate's arithmetic when ensure started a Gateway, or
	// null when it reused one.
	Capacity *gatewayCapacity `json:"capacity"`
	// Note says when a flag was accepted but did not apply.
	Note string `json:"note,omitempty"`
}

func (a *App) newGatewayEnsureCmd() *cobra.Command {
	var (
		force       bool
		fresh       bool
		minTrialRaw string
		timeoutRaw  string
	)
	cmd := &cobra.Command{
		Use:   "ensure",
		Short: "Leave this Instance with a running, healthy Gateway, reusing one when it can",
		Long: `ensure is the one call a harness or an agent makes before e2e work. It reuses the
Instance's Gateway when that is healthy, and starts or resets it otherwise:

  reused   the container runs, the Gateway reports RUNNING, and it accepts the
           Instance API token;
  started  the container was not running: ` + "`up`" + `, then ` + "`wait`" + `;
  reset    the Gateway reports FAULTED, rejects the Instance token (its volume
           predates the token), --fresh was passed, or, with [gateway]
           trial_reset = "off", its trial has less left than --min-trial:
           ` + "`reset`" + `, which discards its data.

The report names the action and the reason, the URL, the Gateway container, the
address the Gateway reaches the host by, and the trial. Under the default
trial_reset = "auto" the trial keeper resets an expired trial in place, so
--min-trial is accepted but never forces a reset, and the report says so.

The Capacity Gate and Consent apply exactly as they do for ` + "`up`" + ` and ` + "`reset`" + `, before
anything is discarded, so a refusal costs nothing.`,
		Example: `  igdev gateway ensure
  igdev gateway ensure --fresh
  igdev gateway ensure --min-trial 30m --json`,
		Args: rejectArgs("gateway ensure"),
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
			minTrial, err := parseMinTrial(minTrialRaw)
			if err != nil {
				return err
			}
			data, err := a.ensureGateway(g, ensureOptions{
				fresh: fresh, force: force, timeout: timeout, minTrial: minTrial,
			})
			if err != nil {
				return err
			}
			a.emit(g.res, data, func() { a.printGatewayEnsure(data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&fresh, "fresh", false,
		"reset the Gateway even when it is healthy, discarding its data")
	cmd.Flags().StringVar(&minTrialRaw, "min-trial", "",
		`with trial_reset = "off", reset when the trial has less than this left: seconds, or a duration like 30m`)
	cmd.Flags().BoolVar(&force, "force", false,
		"start the Gateway even when the Capacity Gate refuses (accepts the out-of-memory risk)")
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait for health: seconds, or a duration like 3m (default 180s)")
	return cmd
}

// ensureDecision inspects the Instance and decides what ensure does. Nothing here
// changes state: a Gateway that is still starting is waited for, which is the only
// side effect, and a running Gateway is only read.
func (a *App) ensureDecision(g *gateway, fresh bool, timeout, minTrial time.Duration, autoTrial bool) (string, string, error) {
	services, fault := g.compose.Ps()
	if fault != nil {
		return "", "", fault
	}
	reported := make([]gatewayService, 0, len(services))
	for _, service := range services {
		reported = append(reported, gatewayService{Service: service.Service, State: service.State})
	}
	running := gatewayState(reported) == "running"
	switch {
	case fresh:
		return ensureReset, ensureFresh, nil
	case !running:
		return ensureStarted, ensureNotRunning, nil
	}

	client := &http.Client{Timeout: gatewayProbeTimeout}
	if state := gatewayReports(client, g.url()); state == faultedState {
		return ensureReset, ensureFaulted, nil
	} else if state != readinessState {
		// Starting, or not answering yet: give it the same deadline `wait` would.
		if err := a.waitForGateway(g, timeout); err != nil {
			if gatewayReports(client, g.url()) == faultedState {
				return ensureReset, ensureFaulted, nil
			}
			return "", "", err
		}
	}
	if g.token != "" && tokenRejected(client, g.url(), g.token) {
		return ensureReset, ensureTokenRejected, nil
	}
	if minTrial > 0 && !autoTrial {
		if state, err := trial.Read(client, g.url()); err == nil && state.LicenseMode == "Trial" &&
			(state.Expired || time.Duration(state.SecondsLeft)*time.Second < minTrial) {
			return ensureReset, ensureTrialShort, nil
		}
	}
	return ensureReused, ensureHealthy, nil
}

// gatewayReports is the state the Gateway reports on /StatusPing, or "" when it
// does not answer.
func gatewayReports(client *http.Client, url string) string {
	resp, err := client.Get(url + readinessPath)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, readinessBodyLimit))
	return reportedState(body)
}

// tokenRejected reports whether the Gateway answered the Instance token with 401
// or 403: a Gateway that does not know it, because its volume predates the seed.
// A Gateway that does not answer at all is not a rejection.
func tokenRejected(client *http.Client, url, token string) bool {
	req, err := http.NewRequest(http.MethodGet, url+tokenProbePath, nil)
	if err != nil {
		return false
	}
	req.Header.Set(apitoken.Header, token)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
}

// parseMinTrial reads --min-trial: bare seconds or a Go duration. Empty is zero,
// which never forces a reset.
func parseMinTrial(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	d, err := parseTimeout(raw, 0)
	if err != nil {
		return 0, contract.UsageFault(fmt.Sprintf("--min-trial %q is not a positive duration", raw),
			contract.Remediation{Command: "igdev help gateway ensure", Why: "show the accepted forms of --min-trial"})
	}
	return d, nil
}

func (a *App) printGatewayEnsure(data gatewayEnsureData) {
	fmt.Fprintf(a.Stdout, "gateway %s (%s): %s\n", data.Action, data.Reason, data.Namespace)
	fmt.Fprintf(a.Stdout, "url:         %s\n", data.URL)
	fmt.Fprintf(a.Stdout, "container:   %s\n", data.Container)
	if data.Trial != nil {
		fmt.Fprintf(a.Stdout, "trial:       %s\n", describeTrial(*data.Trial))
	}
	if data.Note != "" {
		fmt.Fprintf(a.Stdout, "note:        %s\n", data.Note)
	}
}

// ensureOptions are the knobs of one ensure.
type ensureOptions struct {
	fresh    bool
	force    bool
	timeout  time.Duration
	minTrial time.Duration
}

// ensureGateway leaves the Instance with a running, healthy Gateway and reports
// what it did. It is `gateway ensure` and the first step of
// `ci-local --with-gateway`.
func (a *App) ensureGateway(g *gateway, opts ensureOptions) (gatewayEnsureData, error) {
	data := gatewayEnsureData{
		gatewayAddress: g.address(),
		Container:      docker.GatewayContainer(g.compose.Namespace),
		HostAddress:    runtimeassets.HostAddress,
	}
	autoTrial := g.doc.Gateway.EffectiveTrialReset() == project.TrialResetAuto
	if opts.minTrial > 0 && autoTrial {
		data.Note = `--min-trial does not apply: trial_reset = "auto" resets an expired trial in place`
	}

	action, reason, err := a.ensureDecision(g, opts.fresh, opts.timeout, opts.minTrial, autoTrial)
	if err != nil {
		return data, err
	}
	data.Action, data.Reason = action, reason
	if action != ensureReused {
		decision, fault := a.admit(g, opts.force)
		if fault != nil {
			return data, fault
		}
		capacity := capacityOf(decision, opts.force)
		data.Capacity = &capacity
		if action == ensureReset {
			a.stage("gateway ensure: resetting %s (%s)", g.stamp.Namespace(), reason)
			if fault := g.compose.Down(true); fault != nil {
				return data, fault
			}
		}
		a.stage("gateway ensure: starting %s (docker compose up --detach --build %s)",
			g.stamp.Namespace(), strings.Join(g.compose.UpServices(), " "))
		if fault := g.compose.Up(); fault != nil {
			return data, fault
		}
		if err := a.waitForGateway(g, opts.timeout); err != nil {
			return data, err
		}
	}
	data.Trial = readTrial(g.url())
	return data, nil
}
