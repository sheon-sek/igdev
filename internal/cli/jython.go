package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/jython"
	"github.com/sheon-sek/igdev/internal/project"
)

// jythonCheckData is the `data` member of a `jython check` envelope.
type jythonCheckData struct {
	Version string `json:"version"`
	// Jar is the verified cache entry the compile ran against, so a failure can
	// be reproduced against the same artifact.
	Jar       string   `json:"jar"`
	FileCount int      `json:"file_count"`
	Files     []string `json:"files"`
	// Diagnostics is the structured form of the failures; empty on success.
	Diagnostics []jython.Diagnostic `json:"diagnostics,omitempty"`
}

func (a *App) newJythonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jython",
		Short: "Compile project code with the pinned Jython compatibility checker",
		Long: `jython checks Python source with the Jython version the Ignition runtime embeds,
so a construct the runtime cannot parse fails here rather than in the Gateway.

The standalone checker is a downloaded artifact pinned by sha256 in the embedded
version catalog. It is fetched once into the machine-wide cache
(~/.cache/igdev/jython/<version>/), guarded by a per-entry file lock so parallel
igdev processes fetch it exactly once, and re-hashed on every use: bytes that do
not match the pin are quarantined beside the entry and never reused.

` + "`check`" + ` is the ` + "`igdev check`" + ` pipeline's last stage on its own.`,
		Example: `  igdev jython check src/main/python
  igdev jython check ignition/script-python/foo.py --json`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newJythonCheckCmd())
	return cmd
}

func (a *App) newJythonCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <path>...",
		Short: "Compile every .py file under the given paths in one JVM",
		Long: `check compiles every .py file under the given paths with one JVM launch per
invocation, however many files there are. A directory is walked for .py files; a
file argument is compiled as given, whatever its extension.

The output names the file and the line of every file the checker rejected, and the
exit level is 1 when any file failed and 0 when the tree is clean. All files are
checked before the exit level is decided, so one run reports every problem it
found.

The paths are relative to the working directory. Inside a Project Root the
contract's ` + "`[ignition].jython_version`" + ` selects the checker version.`,
		Example: `  igdev jython check src/main/python
  igdev jython check ignition/script-python --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("jython check", "path",
					"igdev jython check src/main/python",
					"name a file or directory to compile")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			res, spec, fault := a.jythonCommandSpec()
			if fault != nil {
				return fault
			}
			files, missing := pythonFiles(args)
			if len(missing) > 0 {
				return contract.NewFault(contract.CodeJythonPathMissing, contract.ExitFailure,
					"jython check path does not exist: "+strings.Join(missing, ", ")).
					WithRemediation(contract.Remediation{
						Command: "igdev jython check <path>",
						Why:     "name an existing file or directory to compile",
					})
			}
			data := jythonCheckData{Version: spec.Version, Jar: spec.JarPath(), FileCount: len(files), Files: files}
			if len(files) == 0 {
				a.emit(res, data, func() { fmt.Fprintf(a.Stdout, "[igdev] No .py files found under the given paths\n") })
				return nil
			}
			jar, fault := jython.Ensure(spec)
			if fault != nil {
				return fault
			}
			diagnostics, fault := jython.Compile(spec, jar, files)
			if fault != nil {
				data.Diagnostics = diagnostics
				return fault.WithData(data)
			}
			a.emit(res, data, func() {
				fmt.Fprintf(a.Stdout, "[igdev] Jython %s parsed %d file(s) successfully\n", spec.Version, len(files))
			})
			return nil
		},
	}
}

// jythonCommandSpec resolves the Jython version and cache wiring for a standalone
// `jython check`: the contract's target inside a Project Root, the embedded
// default outside one.
func (a *App) jythonCommandSpec() (*config.Resolution, jython.Spec, *contract.Fault) {
	found, res, err := a.gate()
	if err != nil {
		return res, jython.Spec{}, contract.AsFault(err)
	}
	version := project.DefaultJythonVersion
	if found.InProject() {
		_, doc, fault := gate.ContractOnly(a.gateInput(found))
		if fault != nil {
			return res, jython.Spec{}, fault
		}
		version = jythonVersion(doc)
	}
	return res, a.jythonSpec(res, version), nil
}
