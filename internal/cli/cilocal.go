package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
)

// ciLocalEventDefault is the GitHub event `ci-local` runs when --event says
// nothing else: the event a pull-request workflow is written against, and the
// one the bash specification hard-coded.
const ciLocalEventDefault = "pull_request"

// ciLocalTailLines is how much of act's output the envelope carries: the last
// lines of the run, the same window `gateway logs --tail` shows.
const ciLocalTailLines = 50

// configKeyActOffline is the config key behind --offline and IGDEV_ACT_OFFLINE.
// It is a resolved setting like any other, so the offline decision comes from
// the five-tier rule (flag > environment > .igdev/local.toml > igdev.toml >
// embedded default) rather than from one privileged variable.
const configKeyActOffline = "act.offline"

// actArgs renders the act command line: the event (act's positional argument),
// the job, the offline pair when the run is offline, and then the caller's
// passthrough arguments verbatim, in order.
func actArgs(event, job string, offline bool, passthrough []string) []string {
	argv := []string{"act", event, "-j", job}
	if offline {
		// An offline run pulls no image and downloads no action.
		argv = append(argv, "--pull=false", "--action-offline-mode")
	}
	return append(argv, passthrough...)
}

// ciLocalData is the `data` member of a `ci-local` envelope. The key set and
// order are frozen by the goldens in itest/testdata/golden.
type ciLocalData struct {
	// Event is the GitHub event handed to act as its positional argument.
	Event string `json:"event"`
	// Job is the workflow job name the run is limited to (`-j`).
	Job string `json:"job"`
	// Offline is the resolved offline decision, whichever tier made it.
	Offline bool `json:"offline"`
	// Command is the invocation as one line, for a human or a log.
	Command string `json:"command"`
	// Args is the same invocation as argv, act first.
	Args []string `json:"args"`
	// Workdir is the Project Root act ran in.
	Workdir string `json:"workdir"`
	// Exit is act's exit code; 0 on success.
	Exit int `json:"exit"`
	// OutputTail is the last ciLocalTailLines lines act printed.
	OutputTail []string `json:"output_tail"`
}

// missingActFault is the fault for a host without act. It names the tool the
// way `igdev doctor` does and how to install it, because the fix is a host
// change, not a project change.
func missingActFault(cause error) *contract.Fault {
	return contract.NewFault(contract.CodeActMissing, contract.ExitFailure,
		"act is not on PATH: igdev ci-local runs this project's GitHub Actions workflows through it").
		WithCause(cause).
		WithRemediation(
			contract.Remediation{Command: "pipx install act", Why: "install act (nektos/act)"},
			contract.Remediation{Command: "brew install act", Why: "install act (nektos/act) on macOS"})
}

// requireCILocalGate is the Gate `ci-local` passes before it runs anything:
// discovery, config resolution, contract schema, and a current Setup Stamp.
//
// Consent is deliberately not checked here: a local workflow run starts no
// Gateway and accepts no license, so the only state it needs is a materialized
// checkout.
func (a *App) requireCILocalGate() (project.Found, *config.Resolution, error) {
	found, res, err := a.gate()
	if err != nil {
		return found, res, err
	}
	if err := gate.Require(a.gateInput(found)); err != nil {
		return found, res, err
	}
	return found, res, nil
}

func (a *App) newCILocalCmd() *cobra.Command {
	var event, job string
	var offline bool
	cmd := &cobra.Command{
		Use:   "ci-local",
		Short: "Run this project's GitHub Actions workflows locally through act",
		Long: `ci-local runs one job of this project's GitHub Actions workflows on the local
machine: act is invoked as a subprocess from the Project Root, and its exit code
is this command's exit code, so a failing workflow fails the run the same way it
fails CI.

The event act runs is ` + "`pull_request`" + ` unless --event says otherwise, and the job is
selected with the required --job flag. ` + "`--offline`" + ` runs without network access —
act is handed ` + "`--pull=false --action-offline-mode`" + ` — and is settable from the flag, the
environment (` + "`IGDEV_ACT_OFFLINE=1`" + `), or the checkout-local tier; the flag wins when they
disagree. The tracked Project Contract cannot carry it: its schema has no ` + "`[act]`" + `
section.

Arguments act should receive go after ` + "`--`" + `: everything after it reaches act verbatim
and in order, so an act flag igdev does not know (` + "`--reuse`" + `,
` + "`--container-architecture`" + `) passes through unchanged. A trailing argument that is not
flag-shaped needs no ` + "`--`" + `; flag-shaped passthrough is what it is for.

act is an optional ` + "`igdev doctor`" + ` prerequisite: when it is not installed,
` + "`ci-local`" + ` fails with IGDEV_E_ACT_MISSING and the install commands, and no
subprocess is attempted. The verb passes the Gate (a current Checkout Setup) but
not Consent: it starts no Gateway.`,
		Example: `  igdev ci-local --job foundation
  igdev ci-local --event push --job foundation --offline
  igdev ci-local --job foundation --json
  igdev ci-local --job foundation -- --reuse --container-architecture linux/amd64`,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			// The flag tier is recorded before RunE resolves config, so an
			// explicit --offline/--offline=false outranks every other tier.
			if cmd.Flags().Changed("offline") {
				a.actOffline = strconv.FormatBool(offline)
			}
			if strings.TrimSpace(job) == "" {
				return missingFlag("ci-local", "--job", "igdev ci-local --job foundation",
					"run one job of this project's workflows")
			}
			// An explicitly empty event is the default event, never an act run
			// with no event at all.
			if strings.TrimSpace(event) == "" {
				event = ciLocalEventDefault
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			found, res, err := a.requireCILocalGate()
			if err != nil {
				return err
			}
			bin, lookErr := exec.LookPath("act")
			if lookErr != nil {
				return missingActFault(lookErr)
			}
			data := ciLocalData{
				Event:   event,
				Job:     job,
				Offline: res.Bool(configKeyActOffline),
				Args:    actArgs(event, job, res.Bool(configKeyActOffline), args),
				Workdir: found.Root,
			}
			data.Command = strings.Join(data.Args, " ")
			return a.runAct(res, bin, data)
		},
	}
	cmd.Flags().StringVar(&event, "event", ciLocalEventDefault,
		"GitHub event act runs")
	cmd.Flags().StringVar(&job, "job", "",
		"workflow job to run (required)")
	cmd.Flags().BoolVar(&offline, "offline", false,
		"run offline: act gets --pull=false --action-offline-mode")
	return cmd
}

// runAct starts act at the Project Root, streams its output, and reports the
// run. In machine mode stdout is the envelope, so act's stdout joins its stderr
// on stderr; in human mode each stream keeps the stream it came from.
func (a *App) runAct(res *config.Resolution, bin string, data ciLocalData) error {
	out, errOut := io.Writer(a.Stdout), io.Writer(a.Stderr)
	if res.IsJSON() {
		// Both of act's streams land on stderr in machine mode, and exec copies
		// them with one goroutine each: one serialized writer keeps two lines
		// from interleaving mid-line.
		shared := &syncWriter{w: a.Stderr}
		out, errOut = shared, shared
	}
	// One tail over both streams: what act printed, in the order igdev saw it.
	tail := newLineTail(ciLocalTailLines)
	child := exec.Command(bin, data.Args[1:]...)
	child.Dir = data.Workdir
	child.Stdout = io.MultiWriter(out, tail)
	child.Stderr = io.MultiWriter(errOut, tail)
	if !res.IsJSON() {
		a.printCILocalStart(data)
	}
	runErr := child.Run()
	data.OutputTail = tail.Lines()
	if runErr != nil {
		exit := 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exit = exitErr.ExitCode()
		}
		data.Exit = exit
		fault := contract.NewFault(contract.CodeActFailed, contract.Exit(exit),
			fmt.Sprintf("act exited with code %d: %s", exit, data.Command)).
			WithCause(runErr).WithData(data)
		return fault
	}
	data.Exit = 0
	a.emit(res, data, func() { a.printCILocalEnd(data) })
	return nil
}

// syncWriter serializes writes from act's two output copiers onto one
// destination.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// printCILocalStart says what is about to run, on stderr: stdout belongs to act.
func (a *App) printCILocalStart(data ciLocalData) {
	offline := ""
	if data.Offline {
		offline = " (offline)"
	}
	fmt.Fprintf(a.Stderr, "[igdev] act %s%s in %s\n", strings.Join(data.Args[1:], " "), offline, data.Workdir)
}

// printCILocalEnd confirms a successful run; a failed one is reported by the
// fault, which names act's exit code.
func (a *App) printCILocalEnd(data ciLocalData) {
	fmt.Fprintf(a.Stderr, "[igdev] act exited %d\n", data.Exit)
}

// lineTail keeps the last limit lines written through it while the same bytes
// stream to their real destination. It is safe for concurrent writers, because
// act's stdout and stderr are copied by separate goroutines.
type lineTail struct {
	mu      sync.Mutex
	limit   int
	lines   []string
	partial string
}

func newLineTail(limit int) *lineTail { return &lineTail{limit: limit} }

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial += string(p)
	for {
		end := strings.IndexByte(t.partial, '\n')
		if end < 0 {
			break
		}
		t.append(strings.TrimSuffix(t.partial[:end], "\r"))
		t.partial = t.partial[end+1:]
	}
	return len(p), nil
}

func (t *lineTail) append(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > t.limit {
		t.lines = t.lines[len(t.lines)-t.limit:]
	}
}

// Lines returns the tail, oldest first, including a last line act left
// unterminated. The result is always a slice, never nil: an act run that printed
// nothing reports an empty list, not null.
func (t *lineTail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.lines)+1)
	out = append(out, t.lines...)
	if t.partial != "" {
		out = append(out, strings.TrimSuffix(t.partial, "\r"))
	}
	if len(out) > t.limit {
		out = out[len(out)-t.limit:]
	}
	return out
}
