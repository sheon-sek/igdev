// Package cli wires the cobra command tree to the frozen agent contract: one
// JSON envelope on stdout in machine mode, human text otherwise, and an exit
// level chosen from the error's contract code.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/updater"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// App is one CLI invocation with its surroundings injected, so a test can run
// the same tree against a scratch environment.
type App struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Environ []string
	Dir     string

	jsonFlag   bool
	configFlag []string
	rawArgs    []string
	// actOffline is the --offline flag's tier value ("true" or "false"), set by
	// `ci-local` before it resolves config, and "" when the flag was not given.
	// It is a string because a flag tier has to distinguish "not passed" from an
	// explicit --offline=false, which outranks a lower tier that asked for
	// offline runs.
	actOffline string
}

// New returns an App bound to the real process surroundings.
func New(stdout, stderr io.Writer, environ []string) *App {
	dir, err := os.Getwd()
	if err != nil {
		dir = string(filepath.Separator)
	}
	return &App{Stdout: stdout, Stderr: stderr, Environ: environ, Dir: dir}
}

// Execute runs args (argv including argv[0]) and returns the exit level.
func Execute(args []string, app *App) int {
	root := app.newRoot()
	app.rawArgs = args
	if len(args) > 1 {
		root.SetArgs(args[1:])
	}
	root.SetOut(app.Stdout)
	root.SetErr(app.Stderr)

	cmd, err := root.ExecuteC()
	if err != nil {
		fault := classify(cmd, err)
		app.report(fault)
		return int(fault.Exit)
	}
	return int(contract.ExitOK)
}

// classify maps whatever cobra returns onto a contract fault. Cobra's own prose
// only ever reaches the message field; codes and exit levels stay ours.
func classify(cmd *cobra.Command, err error) *contract.Fault {
	var f *contract.Fault
	if errors.As(err, &f) {
		return f
	}
	path := "igdev"
	var leaf string
	if cmd != nil {
		path = cmd.CommandPath()
		leaf = cmd.Name()
	}
	message := err.Error()
	if strings.HasPrefix(message, "unknown flag:") || strings.HasPrefix(message, "flag needs an argument:") {
		help := "igdev --help"
		if leaf != "" && leaf != "igdev" {
			help = "igdev help " + leaf
		}
		return contract.UsageFault(path+": "+message,
			contract.Remediation{Command: help, Why: "show the flags this command accepts"}).WithCause(err)
	}
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure, path+": "+message).WithCause(err)
}

// gate is the discovery stage of the Gate plus config resolution. Contract
// schema and Setup Stamp validation live in internal/gate, which this command
// tree feeds through gateInput; `igdev status` reports that verdict, and every
// command that does project work refuses to run on a fault from gate.Require.
func (a *App) gate() (project.Found, *config.Resolution, error) {
	found, err := project.Discover(a.Dir)
	if err != nil {
		return found, nil, err
	}
	res, err := config.Resolve(a.configInput(found))
	if err != nil {
		return found, nil, err
	}
	return found, res, nil
}

// configInput collects the raw tiers of one resolution.
func (a *App) configInput(found project.Found) config.Input {
	in := config.Input{Flags: a.flagTier(), Environ: a.Environ}
	if found.InProject() {
		in.ContractTOML = found.ContractTOML
		in.ContractPath = found.Contract.Path
		in.LocalTOML = found.LocalConfigTOML
		in.LocalPath = found.LocalConfigPath
	}
	return in
}

// resolvedForInit resolves the config tiers for `igdev init`. init is the repair
// path for a Project Contract igdev cannot read, so a contract tier that fails
// to parse is dropped for this resolution and rewritten by the command itself; a
// broken checkout-local tier still fails, because init does not own that file.
func (a *App) resolvedForInit(found project.Found) (*config.Resolution, error) {
	in := a.configInput(found)
	res, err := config.Resolve(in)
	if err == nil || len(in.ContractTOML) == 0 {
		return res, err
	}
	in.ContractTOML, in.ContractPath = nil, ""
	return config.Resolve(in)
}

// resolvedForSetup resolves the config tiers for `igdev setup`. setup writes the
// checkout-local tier, so a .igdev/local.toml that fails to parse is dropped for
// this resolution and rewritten by the command itself; the contract tier still
// fails, because setup does not own that file.
func (a *App) resolvedForSetup(found project.Found) (*config.Resolution, error) {
	in := a.configInput(found)
	res, err := config.Resolve(in)
	if err == nil || len(in.LocalTOML) == 0 {
		return res, err
	}
	in.LocalTOML, in.LocalPath = nil, ""
	return config.Resolve(in)
}

// gateInput assembles the Gate's input from what discovery already read, so a
// command pays for reading the contract and the Checkout Setup record once.
func (a *App) gateInput(found project.Found) gate.Input {
	return gate.Input{
		InProject:      found.InProject(),
		StartDir:       found.StartDir,
		ContractPath:   found.Contract.Path,
		ContractRaw:    found.ContractTOML,
		SetupPresent:   found.Setup,
		SetupPath:      found.SetupPath,
		SetupRaw:       found.SetupRaw,
		SetupReadError: found.SetupReadError,
		CLIVersion:     Version(),
		CLIContract:    contract.Version,
	}
}

// flagTier converts the reserved flags into flag-tier config values.
func (a *App) flagTier() map[string]string {
	out := map[string]string{}
	if a.actOffline != "" {
		out[configKeyActOffline] = a.actOffline
	}
	for _, pair := range a.configFlag {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			// A pair with no "=" is not a key; report it as an unknown key.
			out[strings.TrimSpace(pair)] = ""
			continue
		}
		out[strings.TrimSpace(key)] = value
	}
	switch {
	case a.jsonFlag || a.askedJSON():
		out[config.DialectKey] = "json"
	case a.declaredTextOutput():
		// An explicit --json=false is a flag-tier value, so it outranks a
		// lower tier that asked for JSON.
		out[config.DialectKey] = "text"
	}
	return out
}

// declaredTextOutput reports an explicit --json=false on the command line.
func (a *App) declaredTextOutput() bool {
	for i := 1; i < len(a.rawArgs); i++ {
		if a.rawArgs[i] == "--json=false" {
			return true
		}
	}
	return false
}

// askedJSON reports whether --json appeared on the command line. A flag parse
// that stopped at a bad flag never set the field, so the failure path scans the
// raw arguments to pick the right dialect.
func (a *App) askedJSON() bool {
	for i := 1; i < len(a.rawArgs); i++ {
		switch a.rawArgs[i] {
		case "--json", "--json=true":
			return true
		case "--json=false":
			return false
		}
	}
	return false
}

// wantsJSON decides the dialect for a failure path, where the command never got
// far enough to resolve the whole config. Only output.format is peeled, and a
// tier that cannot state it is skipped: an unrelated bad setting must not stop
// the error arriving as JSON when JSON is what was asked for.
func (a *App) wantsJSON() bool {
	in := config.Input{Flags: a.flagTier(), Environ: a.Environ}
	if found, err := project.Discover(a.Dir); err == nil && found.InProject() {
		in.ContractTOML = found.ContractTOML
		in.ContractPath = found.Contract.Path
		in.LocalTOML = found.LocalConfigTOML
		in.LocalPath = found.LocalConfigPath
	}
	return config.DialectIsJSON(in)
}

// report writes a fault in the dialect the caller asked for. The exit level
// comes from the fault itself.
func (a *App) report(f *contract.Fault) {
	if a.wantsJSON() {
		a.writeEnvelope(contract.Failure(f))
		return
	}
	fmt.Fprintf(a.Stderr, "[igdev] ERROR %s: %s\n", f.Code, f.Message)
	for _, r := range f.Remediation {
		fmt.Fprintf(a.Stderr, "[igdev]   fix: %s  (%s)\n", r.Command, r.Why)
	}
}

func (a *App) writeEnvelope(env contract.Envelope) {
	raw, err := contract.Encode(env)
	if err != nil {
		fmt.Fprintf(a.Stderr, "[igdev] ERROR %s: cannot encode response: %v\n", contract.CodeInternal, err)
		return
	}
	a.Stdout.Write(raw)
}

// emit prints a successful result in the dialect the resolved config asked for,
// then offers the update notice to humans only.
func (a *App) emit(res *config.Resolution, data any, human func()) {
	if res.IsJSON() {
		a.writeEnvelope(contract.Success(data))
	} else {
		human()
	}
	a.updateNotice(res)
}

// updateNotice prints the cached "update available" line on stderr. It is
// skipped entirely in JSON mode, so machine output never depends on the network
// and never varies between runs.
func (a *App) updateNotice(res *config.Resolution) {
	if res.IsJSON() {
		return
	}
	if !res.Bool("updater.enabled") || config.NotifierDisabled(config.EnvironMap(a.Environ)) {
		return
	}
	notice := updater.Notice(updater.Options{
		APIURL:    res.String("updater.releases_api_url"),
		CachePath: filepath.Join(xdg.Resolve().Cache, updater.CacheFileName),
		Current:   Version(),
		TTL:       hours(res.Int("updater.cache_ttl_hours")),
		Timeout:   seconds(res.Int("updater.timeout_seconds")),
	})
	if notice == "" {
		return
	}
	fmt.Fprintf(a.Stderr, "[igdev] %s\n", notice)
}

func hours(n int) time.Duration {
	if n <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(n) * time.Hour
}

func seconds(n int) time.Duration {
	if n <= 0 {
		return 2 * time.Second
	}
	return time.Duration(n) * time.Second
}
