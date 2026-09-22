package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/baseline"
	"github.com/sheon-sek/igdev/internal/capacity"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/localconfig"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/runtimeassets"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The Gateway lifecycle's frozen timings and sizes.
const (
	// gatewayWaitDefault is how long `wait` and `smoke` give a starting Gateway.
	gatewayWaitDefault = 180 * time.Second
	// gatewaySmokeDefault is the shorter wait `smoke` uses for a Gateway that is
	// expected to be up already.
	gatewaySmokeDefault = 60 * time.Second
	// gatewayPollInterval is how often the health check is retried.
	gatewayPollInterval = 500 * time.Millisecond
	// gatewayProbeTimeout bounds one health check, so a socket that accepts and
	// then hangs cannot swallow the whole deadline.
	gatewayProbeTimeout = 2 * time.Second
	// gatewayLogTail is how many log lines a failed wait reports.
	gatewayLogTail = 50
)

// gatewayAddress is the recorded addressing of one Instance: what every
// URL-producing verb reports. Every field comes from the Checkout Setup, so no
// output path can assume a port (ADR 0003).
type gatewayAddress struct {
	Instance  string        `json:"instance_id"`
	Namespace string        `json:"namespace"`
	URL       string        `json:"url"`
	Ports     ports.Triplet `json:"ports"`
}

// gatewayCapacity is the Capacity Gate's arithmetic as the caller saw it.
type gatewayCapacity struct {
	// Measured is false when the host's free memory could not be read.
	Measured    bool `json:"measured"`
	AvailableMB int  `json:"available_mb"`
	RequiredMB  int  `json:"required_mb"`
	HeadroomMB  int  `json:"headroom_mb"`
	// Forced is true when the gate refused and --force bypassed it.
	Forced bool `json:"forced"`
}

// gatewayUpData is the result of starting a Gateway.
type gatewayUpData struct {
	gatewayAddress
	Capacity gatewayCapacity `json:"capacity"`
}

// gatewayDownData is the result of stopping a Gateway.
type gatewayDownData struct {
	Instance  string `json:"instance_id"`
	Namespace string `json:"namespace"`
	Volumes   bool   `json:"volumes_removed"`
}

// gatewayService is one compose service as `gateway status` reports it.
type gatewayService struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

// gatewayStatusData is the compose state of one Instance plus its recorded URL.
type gatewayStatusData struct {
	gatewayAddress
	State    string           `json:"state"`
	Services []gatewayService `json:"services"`
}

// gatewaySmokeCheck is one request `gateway smoke` made.
type gatewaySmokeCheck struct {
	Path   string `json:"path"`
	URL    string `json:"url"`
	Status int    `json:"status"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// gatewaySmokeData is the `gateway smoke` result: every check that ran, in order.
type gatewaySmokeData struct {
	Instance string              `json:"instance_id"`
	URL      string              `json:"url"`
	Checks   []gatewaySmokeCheck `json:"checks"`
}

// gatewayLogsData carries the Gateway log in machine mode, where stdout may not
// carry anything but the envelope.
type gatewayLogsData struct {
	Instance  string `json:"instance_id"`
	Namespace string `json:"namespace"`
	Logs      string `json:"logs"`
}

// gatewayCredentialsData is the only machine-readable home of the admin password.
type gatewayCredentialsData struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// gateway is the validated state every gateway verb works from: the Project
// Contract, the recorded Instance and ports, the credentials Compose
// interpolates, and the compose project they describe.
type gateway struct {
	res      *config.Resolution
	doc      project.Doc
	stamp    gate.Stamp
	compose  docker.Compose
	found    project.Found
	username string
	password string
	// passwordSource names where the password came from, for the human report.
	passwordSource string
}

// address is the recorded addressing this Instance reports.
func (g *gateway) address() gatewayAddress {
	return gatewayAddress{
		Instance:  g.stamp.InstanceID,
		Namespace: g.stamp.Namespace(),
		URL:       g.url(),
		Ports:     g.stamp.Ports,
	}
}

// url is the Instance's Gateway URL, built from the recorded port: the recording
// is the only place a port is ever read from (ADR 0003).
func (g *gateway) url() string {
	return fmt.Sprintf("http://%s:%d", ports.BindAddress, g.stamp.Ports.HTTP)
}

// requireRuntimeFiles refuses to drive Compose when the rendered runtime is gone:
// a Checkout Setup whose files were deleted is repaired by setup, not by compose
// failing on a missing file.
func (g *gateway) requireRuntimeFiles() error {
	if _, err := os.Stat(g.compose.File); err != nil {
		return contract.NewFault(contract.CodeSetupRequired, contract.ExitFailure,
			fmt.Sprintf("the Instance runtime files are missing: %v", err)).
			WithCause(err).
			WithRemediation(contract.Remediation{
				Command: "igdev setup",
				Why:     "re-render the Compose files for this Instance",
			})
	}
	return nil
}

// gatewayContext enforces everything every gateway verb requires before it does
// work: a current Checkout Setup (the Gate) and recorded Consent. A verb can
// therefore never act on a checkout, or a machine, that has not passed both.
func (a *App) gatewayContext() (*gateway, error) {
	found, err := project.Discover(a.Dir)
	if err != nil {
		return nil, err
	}
	res, err := config.Resolve(a.configInput(found))
	if err != nil {
		return nil, err
	}
	if err := gate.Require(a.gateInput(found)); err != nil {
		return nil, err
	}
	doc, err := project.ParseDoc(found.ContractTOML, found.Contract.Path)
	if err != nil {
		return nil, err
	}
	doc = doc.Filled(project.DefaultDoc())

	// Running an Ignition Gateway is governed by the EULA, so every verb stops at
	// the same human-required fault setup does, with the same accept command.
	if fault := consent.Check(consent.Path(xdg.Resolve().Config), consent.EULA); fault != nil {
		return nil, fault
	}

	stamp, ok := gate.Decode(found.SetupRaw)
	if !ok {
		// The Gate admits only a current record, so this is unreachable through
		// discovery; failing closed still beats acting on a record igdev cannot read.
		return nil, contract.NewFault(contract.CodeSetupRequired, contract.ExitFailure,
			fmt.Sprintf("the Checkout Setup record %s cannot be read", found.SetupPath)).
			WithRemediation(contract.Remediation{
				Command: "igdev setup",
				Why:     "re-materialize the Checkout Setup",
			})
	}

	credentials, _ := localconfig.Load(found.LocalConfigTOML)
	environ := config.EnvironMap(a.Environ)
	username := firstNonEmpty(environ[localconfig.EnvUsername], credentials.Username, localconfig.DefaultUsername)
	password := firstNonEmpty(environ[localconfig.EnvPassword], credentials.Password)
	source := "local config " + found.LocalConfigPath
	if environ[localconfig.EnvPassword] != "" {
		source = "environment " + localconfig.EnvPassword
	}

	runtimeDir := filepath.Join(found.Root, project.StateDir, "runtime")
	// A staged Baseline rides with every verb: the Compose file's command
	// interpolates the restore arguments, and the same variable is empty when
	// nothing is staged, so `reset` restores from the staged file and `up` after
	// `baseline clear` restores nothing — the staged file is the state.
	restoreArgs := ""
	if baseline.Read(baseline.Dir(filepath.Join(found.Root, project.StateDir))).Staged {
		restoreArgs = baseline.Args()
	}
	return &gateway{
		found: found, res: res, doc: doc, stamp: stamp,
		username: username, password: password, passwordSource: source,
		compose: docker.Compose{
			Namespace: stamp.Namespace(),
			File:      filepath.Join(runtimeDir, runtimeassets.ComposeFileName),
			EnvFile:   filepath.Join(runtimeDir, runtimeassets.EnvFileName),
			Dir:       found.Root,
			// The credentials and the Baseline restore arguments reach Compose
			// from the process environment, which is why no rendered file carries
			// a secret and no rendered file has to be rewritten when the staged
			// Baseline changes.
			Env: []string{
				"GATEWAY_ADMIN_USERNAME=" + username,
				"GATEWAY_ADMIN_PASSWORD=" + password,
				"GATEWAY_RESTORE_ARGS=" + restoreArgs,
			},
		},
	}, nil
}

// admit applies the Capacity Gate (ADR 0003) and reports the numbers it saw. A
// gate that could not measure, or that --force bypassed, says so on stderr: a
// guard that silently did not guard is worse than no guard.
func (a *App) admit(g *gateway, force bool) (capacity.Decision, *contract.Fault) {
	decision, err := capacity.Check(g.doc.Gateway.MemoryMB)
	switch {
	case err != nil:
		fmt.Fprintf(a.Stderr, "[igdev] WARNING the Capacity Gate cannot measure free memory (%v); starting anyway\n", err)
	case !decision.Meets() && force:
		fmt.Fprintf(a.Stderr, "[igdev] WARNING the Capacity Gate is bypassed (--force): %s\n", decision.Summary())
	case !decision.Meets():
		return decision, capacityFault(decision)
	}
	return decision, nil
}

// capacityFault is the human-required refusal: a person frees memory or accepts
// the risk, so the exit level is 3 and the message names what is running.
func capacityFault(d capacity.Decision) *contract.Fault {
	message := fmt.Sprintf("the Capacity Gate refused to start a Gateway: %s", d.Summary())
	if running := docker.Running(); len(running) > 0 {
		message += "; running igdev instances: " + strings.Join(running, ", ")
	} else {
		message += "; no other igdev Gateway is running on this machine"
	}
	return contract.NewFault(contract.CodeCapacity, contract.ExitHumanAction, message).
		WithRemediation(
			contract.Remediation{
				Command: "igdev gateway up --force",
				Why:     "start anyway, accepting the out-of-memory risk",
			},
			contract.Remediation{
				Command: "igdev gateway down --volumes",
				Why:     "stop this checkout's Gateway and give its memory back",
			})
}

// capacityOf renders the decision for the caller.
func capacityOf(d capacity.Decision, force bool) gatewayCapacity {
	return gatewayCapacity{
		Measured:    d.Measured,
		AvailableMB: d.AvailableMB,
		RequiredMB:  d.RequiredMB(),
		HeadroomMB:  d.HeadroomMB,
		Forced:      force && !d.Meets(),
	}
}

func (a *App) newGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Control this Instance's Ignition Gateway",
		Long: `gateway drives the Ignition Gateway container the Checkout Setup describes: one
compose project named igdev-<instance>, one named volume, and the recorded ports.

Every verb passes the Gate (a current Checkout Setup) and the machine's Consent
record first, and every URL it prints is the recorded one — the port in
` + "`.igdev/setup.json`" + `, never an assumed 8088 (ADR 0003). ` + "`up`" + ` and ` + "`reset`" + ` also pass the
Capacity Gate: when the host's free memory is below the requested heap plus 512 MiB
headroom, starting another Gateway is refused with IGDEV_E_CAPACITY at exit level 3
until memory is freed or ` + "`--force`" + ` accepts the risk.

The container engine is only ever addressed through this Instance's project:
` + "`docker compose`" + ` is given the project name, the Compose file ` + "`igdev setup`" + `
rendered, and that file's environment file (` + "`--project-name`" + `, ` + "`--file`" + `,
` + "`--env-file`" + `), with the admin credentials supplied from the process environment and
never from a file on disk.`,
		Example: `  igdev gateway up
  igdev gateway wait --timeout 240
  igdev gateway smoke
  igdev gateway url
  igdev gateway status --json
  igdev gateway down --volumes`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		a.newGatewayUpCmd(),
		a.newGatewayDownCmd(),
		a.newGatewayResetCmd(),
		a.newGatewayRestartCmd(),
		a.newGatewayWaitCmd(),
		a.newGatewaySmokeCmd(),
		a.newGatewayStatusCmd(),
		a.newGatewayLogsCmd(),
		a.newGatewayURLCmd(),
		a.newGatewayCredentialsCmd(),
	)
	return cmd
}

func (a *App) newGatewayUpCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Build if needed and start this Instance's Gateway",
		Long: `up starts the Instance: the Capacity Gate first, then a detached ` + "`docker compose up`" + `
that builds when the image is not current, for this Instance's project, with the
recorded ports and the generated admin credentials. The Gateway is not waited for —
use ` + "`igdev gateway wait`" + `, which is also the last step of ` + "`igdev gateway reset`" + `.

--force starts the Gateway even when the Capacity Gate refuses, which is the
escape hatch for a machine where the measurement is wrong or the risk is accepted.`,
		Example: `  igdev gateway up
  igdev gateway up --force
  igdev gateway up --json`,
		Args: rejectArgs("gateway up"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			decision, fault := a.admit(g, force)
			if fault != nil {
				return fault
			}
			if fault := g.compose.Up(); fault != nil {
				return fault
			}
			data := gatewayUpData{gatewayAddress: g.address(), Capacity: capacityOf(decision, force)}
			a.emit(g.res, data, func() { a.printGatewayStart("up", data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"start the Gateway even when the Capacity Gate refuses (accepts the out-of-memory risk)")
	return cmd
}

func (a *App) newGatewayDownCmd() *cobra.Command {
	var volumes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop this Instance's Gateway",
		Long: `down stops and removes this Instance's containers. ` + "`--volumes`" + ` also removes
the named volume, which discards the Gateway's data — that is what ` + "`reset`" + ` is for.`,
		Example: `  igdev gateway down
  igdev gateway down --volumes
  igdev gateway down --json`,
		Args: rejectArgs("gateway down"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			if fault := g.compose.Down(volumes); fault != nil {
				return fault
			}
			data := gatewayDownData{Instance: g.stamp.InstanceID, Namespace: g.stamp.Namespace(), Volumes: volumes}
			a.emit(g.res, data, func() { a.printGatewayDown(data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&volumes, "volumes", false,
		"also remove the named volume, discarding the Gateway's data")
	return cmd
}

func (a *App) newGatewayResetCmd() *cobra.Command {
	var (
		force      bool
		timeoutRaw string
	)
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Recreate this Instance's Gateway from scratch",
		Long: `reset is ` + "`down --volumes`" + `, then ` + "`up`" + `, then ` + "`wait`" + `: the Gateway's data is
discarded and a fresh container starts against the same recorded ports.

The Capacity Gate is applied before anything is discarded, so a refusal never
costs the data that was about to be reset.`,
		Example: `  igdev gateway reset
  igdev gateway reset --timeout 300
  igdev gateway reset --json`,
		Args: rejectArgs("gateway reset"),
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
			decision, fault := a.admit(g, force)
			if fault != nil {
				return fault
			}
			if fault := g.compose.Down(true); fault != nil {
				return fault
			}
			if fault := g.compose.Up(); fault != nil {
				return fault
			}
			if err := a.waitForGateway(g, timeout); err != nil {
				return err
			}
			data := gatewayUpData{gatewayAddress: g.address(), Capacity: capacityOf(decision, force)}
			a.emit(g.res, data, func() { a.printGatewayStart("reset", data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"start the Gateway even when the Capacity Gate refuses (accepts the out-of-memory risk)")
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait for health: seconds, or a duration like 3m (default 180s)")
	return cmd
}

func (a *App) newGatewayRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart this Instance's Gateway container",
		Long: `restart restarts the Gateway container in place, keeping its named volume and its
recorded ports.`,
		Example: `  igdev gateway restart
  igdev gateway restart --json`,
		Args: rejectArgs("gateway restart"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			if fault := g.compose.Restart(); fault != nil {
				return fault
			}
			data := g.address()
			a.emit(g.res, data, func() {
				fmt.Fprintf(a.Stdout, "gateway restarted: %s  %s\n", data.Namespace, data.URL)
			})
			return nil
		},
	}
	return cmd
}

func (a *App) newGatewayWaitCmd() *cobra.Command {
	var timeoutRaw string
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait until this Instance's Gateway answers HTTP",
		Long: `wait polls the recorded Gateway URL until it answers with anything below 400, or
until the timeout (default 180s) expires. A redirect counts: a fresh Gateway answers
its root document with a redirect to the web UI.

On timeout the command reports the tail of the Gateway's own log, because the reason
a Gateway never came up is in that log.`,
		Example: `  igdev gateway wait
  igdev gateway wait --timeout 240
  igdev gateway wait --timeout 3m --json`,
		Args: rejectArgs("gateway wait"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			timeout, err := parseTimeout(timeoutRaw, gatewayWaitDefault)
			if err != nil {
				return err
			}
			if err := a.waitForGateway(g, timeout); err != nil {
				return err
			}
			data := g.address()
			a.emit(g.res, data, func() { fmt.Fprintf(a.Stdout, "gateway ready: %s\n", data.URL) })
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait: seconds, or a duration like 3m (default 180s)")
	return cmd
}

func (a *App) newGatewaySmokeCmd() *cobra.Command {
	var timeoutRaw string
	cmd := &cobra.Command{
		Use:   "smoke",
		Short: "Check the Gateway's root document and this project's declared endpoints",
		Long: `smoke waits (default 60s) for the recorded Gateway URL, then requests the root
document and every path the Project Contract declares in ` + "`[gateway] smoke_endpoints`" + `,
in that order. A contract that declares none checks the root alone.

A check fails when the Gateway answers 400 or above, or does not answer at all; the
failure names the endpoint that failed.`,
		Example: `  igdev gateway smoke
  igdev gateway smoke --json`,
		Args: rejectArgs("gateway smoke"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			timeout, err := parseTimeout(timeoutRaw, gatewaySmokeDefault)
			if err != nil {
				return err
			}
			if err := a.waitForGateway(g, timeout); err != nil {
				return err
			}
			checks := a.runSmokeChecks(g)
			if failed, ok := failedCheck(checks); ok {
				passed := len(checks) - countFailed(checks)
				return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
					fmt.Sprintf("gateway smoke failed: %s (%d of %d checks passed)",
						failed.reason(), passed, len(checks))).
					WithRemediation(
						contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read the Gateway's own log"},
						contract.Remediation{Command: "igdev gateway status --json", Why: "report the compose service state"})
			}
			data := gatewaySmokeData{Instance: g.stamp.InstanceID, URL: g.url(), Checks: checks}
			a.emit(g.res, data, func() { a.printSmoke(data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait for health first: seconds, or a duration like 3m (default 60s)")
	return cmd
}

func (a *App) newGatewayStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report this Instance's compose state and recorded URL",
		Long: `status reports what the container engine says about this Instance's compose project
and the URL the Checkout Setup recorded, so an agent never has to read setup.json or
assume a port.`,
		Example: `  igdev gateway status
  igdev gateway status --json`,
		Args: rejectArgs("gateway status"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			services, fault := g.compose.Ps()
			if fault != nil {
				return fault
			}
			reported := make([]gatewayService, 0, len(services))
			for _, service := range services {
				reported = append(reported, gatewayService{
					Name: service.Name, Service: service.Service, State: service.State, Status: service.Status,
				})
			}
			data := gatewayStatusData{gatewayAddress: g.address(), State: gatewayState(reported), Services: reported}
			a.emit(g.res, data, func() { a.printGatewayStatus(data) })
			return nil
		},
	}
	return cmd
}

func (a *App) newGatewayLogsCmd() *cobra.Command {
	var tail int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print this Instance's Gateway log",
		Long: `logs passes the Gateway container's log through. In human mode it streams to stdout;
with --json the same text arrives as the envelope's ` + "`data.logs`" + `, because stdout may
then carry nothing but the envelope.`,
		Example: `  igdev gateway logs
  igdev gateway logs --tail 200
  igdev gateway logs --tail 50 --json`,
		Args: rejectArgs("gateway logs"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRuntimeFiles(); err != nil {
				return err
			}
			out, fault := g.compose.Logs(tail)
			if fault != nil {
				return fault
			}
			data := gatewayLogsData{Instance: g.stamp.InstanceID, Namespace: g.stamp.Namespace(), Logs: out}
			a.emit(g.res, data, func() { fmt.Fprint(a.Stdout, out) })
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 0, "print only the last N lines (0 prints everything the engine holds)")
	return cmd
}

func (a *App) newGatewayURLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print this Instance's recorded Gateway URL",
		Long: `url prints the Gateway URL recorded in the Checkout Setup: the loopback address and
the port allocated to this Instance. Nothing in igdev assumes 8088 (ADR 0003), so
this is the only URL to script against.`,
		Example: `  igdev gateway url
  curl "$(igdev gateway url)/"`,
		Args: rejectArgs("gateway url"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			data := g.address()
			a.emit(g.res, data, func() { fmt.Fprintf(a.Stdout, "%s\n", data.URL) })
			return nil
		},
	}
	return cmd
}

func (a *App) newGatewayCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Read the Gateway admin credentials (the password only as JSON)",
		Long: `credentials reports the Gateway admin username and the source of the password.

The password itself is printed only with --json, so it never lands in a terminal's
scrollback, a shell history, or a CI log: this is the one machine-readable way to
obtain it. The value comes from IGDEV_GATEWAY_ADMIN_PASSWORD when that is set, and
from the 0600 .igdev/local.toml setup wrote otherwise.`,
		Example: `  igdev gateway credentials --json
  curl -u "$(igdev gateway credentials --json | jq -r '.data.username'):..." http://.../`,
		Args: rejectArgs("gateway credentials"),
		RunE: func(_ *cobra.Command, _ []string) error {
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if g.password == "" {
				return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
					fmt.Sprintf("no Gateway admin password is recorded (%s)", g.passwordSource)).
					WithRemediation(contract.Remediation{
						Command: "igdev setup",
						Why:     "generate the Gateway admin credentials",
					})
			}
			if g.res.IsJSON() {
				a.emit(g.res, gatewayCredentialsData{Username: g.username, Password: g.password}, func() {})
				return nil
			}
			a.printCredentials(g)
			a.updateNotice(g.res)
			return nil
		},
	}
	return cmd
}

// waitForGateway polls the recorded URL until the Gateway answers or the deadline
// passes, then reports the log tail: a Gateway that never came up says why only in
// its own log.
func (a *App) waitForGateway(g *gateway, timeout time.Duration) error {
	url := g.url() + "/"
	client := &http.Client{Timeout: gatewayProbeTimeout}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		_, last = probe(client, url)
		if last == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(gatewayPollInterval)
	}
	if tail, fault := g.compose.Logs(gatewayLogTail); fault == nil && strings.TrimSpace(tail) != "" {
		fmt.Fprintf(a.Stderr, "[igdev] last %d log lines of %s:\n%s", gatewayLogTail, g.compose.Namespace, tail)
	}
	return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
		fmt.Sprintf("the Gateway at %s did not answer within %s (%v)", g.url(), timeout, last)).
		WithRemediation(
			contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read the Gateway's own log"},
			contract.Remediation{Command: "igdev gateway status --json", Why: "report the compose service state"})
}

// probe performs one health check and returns the status the Gateway answered.
// Anything below 400 is healthy: a fresh Gateway's root document is a redirect to
// the web UI.
func probe(client *http.Client, url string) (int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// runSmokeChecks requests the root document and every declared endpoint, in the
// order the Project Contract declares them.
func (a *App) runSmokeChecks(g *gateway) []gatewaySmokeCheck {
	paths := append([]string{"/"}, g.doc.Gateway.SmokeEndpoints...)
	client := &http.Client{Timeout: gatewayProbeTimeout}
	checks := make([]gatewaySmokeCheck, 0, len(paths))
	for _, path := range paths {
		check := gatewaySmokeCheck{Path: path, URL: g.url() + path}
		status, err := probe(client, check.URL)
		check.Status, check.OK = status, err == nil
		if err != nil {
			check.Error = err.Error()
		}
		checks = append(checks, check)
	}
	return checks
}

// failedCheck returns the first check that did not pass.
func failedCheck(checks []gatewaySmokeCheck) (gatewaySmokeCheck, bool) {
	for _, check := range checks {
		if !check.OK {
			return check, true
		}
	}
	return gatewaySmokeCheck{}, false
}

func countFailed(checks []gatewaySmokeCheck) int {
	failed := 0
	for _, check := range checks {
		if !check.OK {
			failed++
		}
	}
	return failed
}

// reason states why one check failed, naming the endpoint: the message an agent
// reads has to say which request was wrong, not only that smoke failed.
func (c gatewaySmokeCheck) reason() string {
	if c.Error != "" {
		return fmt.Sprintf("GET %s: %s", c.URL, c.Error)
	}
	return fmt.Sprintf("GET %s answered %d", c.URL, c.Status)
}

// gatewayState summarizes the compose services into one word.
func gatewayState(services []gatewayService) string {
	if len(services) == 0 {
		return "absent"
	}
	for _, service := range services {
		if service.State == "running" {
			return "running"
		}
	}
	return "stopped"
}

// parseTimeout reads --timeout: bare seconds, as the legacy interface took them, or
// a Go duration like 3m. An empty value keeps the command's default.
func parseTimeout(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0, timeoutFault(raw)
		}
		return time.Duration(seconds) * time.Second, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		return 0, timeoutFault(raw)
	}
	return timeout, nil
}

func timeoutFault(raw string) *contract.Fault {
	return contract.UsageFault(fmt.Sprintf("--timeout %q is not a positive duration", raw),
		contract.Remediation{Command: "igdev help gateway wait", Why: "show the accepted forms of --timeout"})
}

// printGatewayStart tells a human a Gateway started, with the Capacity Gate's
// arithmetic so the guard is visible even when it passed.
func (a *App) printGatewayStart(verb string, data gatewayUpData) {
	fmt.Fprintf(a.Stdout, "gateway %s: %s\n", verb, data.Namespace)
	fmt.Fprintf(a.Stdout, "url:         %s\n", data.URL)
	switch {
	case !data.Capacity.Measured:
		fmt.Fprint(a.Stdout, "capacity:    not measured\n")
	case data.Capacity.Forced:
		fmt.Fprintf(a.Stdout, "capacity:    %d MiB available, %d MiB required (gate bypassed with --force)\n",
			data.Capacity.AvailableMB, data.Capacity.RequiredMB)
	default:
		fmt.Fprintf(a.Stdout, "capacity:    %d MiB available, %d MiB required\n",
			data.Capacity.AvailableMB, data.Capacity.RequiredMB)
	}
}

func (a *App) printGatewayDown(data gatewayDownData) {
	fmt.Fprintf(a.Stdout, "gateway down: %s\n", data.Namespace)
	if data.Volumes {
		fmt.Fprint(a.Stdout, "volumes:      removed\n")
		return
	}
	fmt.Fprint(a.Stdout, "volumes:      kept (use --volumes to discard the Gateway's data)\n")
}

func (a *App) printGatewayStatus(data gatewayStatusData) {
	fmt.Fprintf(a.Stdout, "gateway:  %s  %s\n", data.Namespace, data.State)
	fmt.Fprintf(a.Stdout, "url:      %s\n", data.URL)
	if len(data.Services) == 0 {
		fmt.Fprint(a.Stdout, "services: none\n")
		return
	}
	fmt.Fprint(a.Stdout, "services:\n")
	for _, service := range data.Services {
		fmt.Fprintf(a.Stdout, "  %-10s %-10s %s\n", service.Service, service.State, service.Status)
	}
}

func (a *App) printSmoke(data gatewaySmokeData) {
	fmt.Fprintf(a.Stdout, "smoke: %s\n", data.URL)
	for _, check := range data.Checks {
		fmt.Fprintf(a.Stdout, "  ok   GET %-24s %d\n", check.Path, check.Status)
	}
}

// printCredentials tells a human where the credential lives without revealing it.
// The password is readable only through --json, so no terminal, shell history, or
// CI log ever carries it.
func (a *App) printCredentials(g *gateway) {
	fmt.Fprintf(a.Stdout, "username: %s\n", g.username)
	fmt.Fprint(a.Stdout, "password: <not printed; re-run with --json to read it>\n")
	fmt.Fprintf(a.Stdout, "source:   %s\n", g.passwordSource)
}
