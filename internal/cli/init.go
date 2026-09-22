package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/textdiff"
)

// diffContext is how much unchanged context a printed diff carries: enough to
// place the change, little enough to read.
const diffContext = 3

// The managed .gitignore entry that keeps a Checkout Setup out of git. The
// entry is matched in any of git's spellings, so a repository that already
// ignores `.igdev` is never given a second line.
const (
	gitignoreMark  = "# igdev: disposable checkout state (managed entry, do not edit)"
	gitignoreEntry = ".igdev/"
)

// initFile is what one tracked write did: which file, whether it was created,
// updated, or already correct, and the diff that shows the change.
type initFile struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	// Digest is the Contract Digest after the write; gitignore files have none.
	Digest string `json:"digest,omitempty"`
	// Diff is the unified diff for this file, empty when nothing changed.
	Diff string `json:"diff"`
}

// initData is the `data` member of a successful `igdev init` envelope.
type initData struct {
	Contract  initFile `json:"contract"`
	Gitignore initFile `json:"gitignore"`
}

func (a *App) newInitCmd() *cobra.Command {
	var (
		name             string
		ignitionVersion  string
		jythonVersion    string
		edition          string
		modules          []string
		scanJython       []string
		scanCapabilities []string
		commandCheck     string
		commandTest      string
		commandBuild     string
		commandSmoke     string
		gatewayMemoryMB  int
		gatewayTimezone  string
		minVersion       string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create or edit the Project Contract (igdev.toml)",
		Long: `init writes the Project Contract: the tracked declaration of how this repository
uses igdev. It is the only command that mutates tracked files, and every write prints
a unified diff — on stderr for humans, inside data for agents — so contract changes
stay reviewable.

The first run in a repository writes igdev.toml with the schema v1 defaults:
Ignition ` + project.DefaultDoc().Ignition.Version + `, Jython ` + project.DefaultJythonVersion + `, edition ` + project.DefaultEdition + `, ` + fmt.Sprint(project.DefaultGatewayMemoryMB) + ` MiB of Gateway heap, timezone ` + project.DefaultTimezone + `,
sample scan paths. It also adds the managed ` + gitignoreEntry + ` entry to .gitignore. A later run
is an edit: a flag overrides the value it names, everything else the contract already
holds is preserved, and a run that changes nothing writes nothing and prints nothing.
Pass an empty value (--modules "" or --command-check "") to clear a field.

Without --json, init never prompts: an agent's fully specified invocation is the whole
interface. The init Wizard arrives in ticket 10.

init and setup are the Gate's repair paths: they run whatever the contract says, so a
hand-edited or future-schema contract can always be rewritten. Every other project
command passes the Gate first and refuses to run on a missing or stale Checkout Setup.`,
		Example: `  igdev init
  igdev init --ignition-version 8.1.21 --modules com.inductiveautomation.perspective
  igdev init --command-check "./gradlew check" --command-test "./gradlew test"
  igdev init --json`,
		Args: rejectArgs("init"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, err := project.Discover(a.Dir)
			if err != nil {
				return err
			}
			res, err := a.resolvedForInit(found)
			if err != nil {
				return err
			}
			root := found.Root
			if root == "" {
				root = a.Dir
			}
			contractPath := filepath.Join(root, project.ContractFile)

			flagValues := project.Doc{
				Project: project.Project{Name: name},
				Tool:    project.Tool{MinVersion: minVersion},
				Ignition: project.Ignition{
					Version:       ignitionVersion,
					JythonVersion: jythonVersion,
					Edition:       edition,
				},
				Modules: project.Modules{Enabled: nonEmpty(modules)},
				Scan:    project.Scan{Jython: nonEmpty(scanJython), Capabilities: nonEmpty(scanCapabilities)},
				Commands: project.Commands{
					Check: commandCheck,
					Test:  commandTest,
					Build: commandBuild,
					Smoke: commandSmoke,
				},
				Gateway: project.Gateway{MemoryMB: gatewayMemoryMB, Timezone: gatewayTimezone},
			}
			// A bad value typed on the command line is the invocation, so it is
			// a usage error; a bad value the contract already held is machine
			// state, and Validate reports that one as IGDEV_E_CONFIG_INVALID.
			if err := initDoc(cmd, project.Found{}, flagValues).Validate(""); err != nil {
				fault := contract.AsFault(err)
				return contract.UsageFault(fault.Message,
					contract.Remediation{Command: "igdev help init", Why: "show the flags init accepts"})
			}
			// Flags are applied over the file's values; the defaults fill whatever
			// both left unset, so an empty flag resets a field instead of writing
			// a value igdev itself would refuse to read.
			doc := initDoc(cmd, found, flagValues).Filled(project.DefaultDoc())
			if err := doc.Validate(contractPath); err != nil {
				return err
			}
			rendered := doc.Render()

			contractFile, err := writeTracked(contractPath, project.ContractFile,
				found.ContractTOML, rendered, 0o644)
			if err != nil {
				return err
			}
			contractFile.Digest = project.Digest(rendered)

			gitignoreFile, err := writeGitignore(root)
			if err != nil {
				return err
			}

			data := initData{Contract: contractFile, Gitignore: gitignoreFile}
			a.emit(res, data, func() { a.printInit(data) })
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&name, "name", "", "repository name recorded as [project].name")
	flags.StringVar(&ignitionVersion, "ignition-version", "",
		"Ignition version this checkout targets, e.g. 8.3.8 (default "+project.DefaultDoc().Ignition.Version+")")
	flags.StringVar(&jythonVersion, "jython-version", "",
		"Jython version the compatibility checker targets (default "+project.DefaultJythonVersion+")")
	flags.StringVar(&edition, "edition", "", "Ignition module edition (default "+project.DefaultEdition+")")
	flags.StringSliceVar(&modules, "modules", nil,
		"module ids to enable, comma-separated or repeated (default: none)")
	flags.StringSliceVar(&scanJython, "scan-jython", nil,
		"directories scanned for Jython sources, comma-separated (default "+strings.Join(project.DefaultScanPaths, ",")+")")
	flags.StringSliceVar(&scanCapabilities, "scan-capabilities", nil,
		"directories scanned for capability usage, comma-separated (default "+strings.Join(project.DefaultScanPaths, ",")+")")
	flags.StringVar(&commandCheck, "command-check", "", "command igdev check runs for this project")
	flags.StringVar(&commandTest, "command-test", "", "command igdev test runs for this project")
	flags.StringVar(&commandBuild, "command-build", "", "command igdev build runs for this project")
	flags.StringVar(&commandSmoke, "command-smoke", "", "command igdev smoke runs after a Gateway starts")
	flags.IntVar(&gatewayMemoryMB, "gateway-memory-mb", 0,
		"Gateway heap in MiB (default "+fmt.Sprint(project.DefaultGatewayMemoryMB)+")")
	flags.StringVar(&gatewayTimezone, "gateway-timezone", "",
		"Gateway timezone, e.g. UTC (default "+project.DefaultTimezone+")")
	flags.StringVar(&minVersion, "tool-min-version", "",
		"oldest igdev version this project accepts, recorded as [tool].min_version")

	return cmd
}

// initDoc builds the contract to write: the existing file's values, the schema
// defaults for whatever it leaves out, and the flags that were typed, in that
// order of increasing authority. A contract igdev cannot carry forward — invalid
// TOML, or a schema this binary does not speak — is replaced wholesale rather
// than parsed partially, and the printed diff shows exactly what changed.
func initDoc(cmd *cobra.Command, found project.Found, flagValues project.Doc) project.Doc {
	doc := project.DefaultDoc()
	if len(found.ContractTOML) > 0 && project.DeclaredSchema(found.ContractTOML) == project.LatestSchema {
		if existing, err := project.DecodeDoc(found.ContractTOML); err == nil {
			doc = existing.Filled(doc)
		}
	}
	doc.Schema = project.LatestSchema

	set := func(flag string, apply func()) {
		if cmd.Flags().Changed(flag) {
			apply()
		}
	}
	set("name", func() { doc.Project.Name = flagValues.Project.Name })
	set("tool-min-version", func() { doc.Tool.MinVersion = flagValues.Tool.MinVersion })
	set("ignition-version", func() { doc.Ignition.Version = flagValues.Ignition.Version })
	set("jython-version", func() { doc.Ignition.JythonVersion = flagValues.Ignition.JythonVersion })
	set("edition", func() { doc.Ignition.Edition = flagValues.Ignition.Edition })
	set("modules", func() { doc.Modules.Enabled = flagValues.Modules.Enabled })
	set("scan-jython", func() { doc.Scan.Jython = flagValues.Scan.Jython })
	set("scan-capabilities", func() { doc.Scan.Capabilities = flagValues.Scan.Capabilities })
	set("command-check", func() { doc.Commands.Check = flagValues.Commands.Check })
	set("command-test", func() { doc.Commands.Test = flagValues.Commands.Test })
	set("command-build", func() { doc.Commands.Build = flagValues.Commands.Build })
	set("command-smoke", func() { doc.Commands.Smoke = flagValues.Commands.Smoke })
	set("gateway-memory-mb", func() { doc.Gateway.MemoryMB = flagValues.Gateway.MemoryMB })
	set("gateway-timezone", func() { doc.Gateway.Timezone = flagValues.Gateway.Timezone })
	return doc
}

// nonEmpty drops the empty entries pflag leaves behind for `--flag ""`, so an
// empty value clears a list instead of adding a blank element to it.
func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// writeTracked writes a tracked file atomically — a temp file in the target
// directory, then a rename, never a backup file — and reports what happened. A
// file that is already byte-identical is left untouched: a second init run is
// not a change. An existing file keeps its permission bits; a new one gets
// newMode.
func writeTracked(path, name string, existing, data []byte, newMode os.FileMode) (initFile, error) {
	out := initFile{Path: path}
	switch {
	case existing != nil && bytes.Equal(existing, data):
		out.Action = "unchanged"
		return out, nil
	case existing == nil:
		out.Action = "created"
	default:
		out.Action = "updated"
	}
	out.Diff = textdiff.Unified(name, existing, data, diffContext)
	mode := newMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(path, data, mode); err != nil {
		return initFile{}, err
	}
	return out, nil
}

// writeAtomic replaces path with data in one rename. Nothing observes a partial
// file, a crash leaves the old bytes, and no .bak is ever created.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := atomicfile.Write(path, data, mode, 0o755); err != nil {
		return writeFault(path, err)
	}
	return nil
}

func writeFault(path string, err error) *contract.Fault {
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
		fmt.Sprintf("cannot write %s: %v", path, err)).WithCause(err)
}

// writeGitignore maintains the managed .igdev/ entry in the repository's
// .gitignore, creating the file when a repository has none. The entry is added
// once: any spelling that already ignores .igdev is left alone.
func writeGitignore(root string) (initFile, error) {
	path := filepath.Join(root, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return initFile{}, writeFault(path, err)
	}
	if os.IsNotExist(err) {
		existing = nil
	}
	if ignoresStateDir(existing) {
		return initFile{Path: path, Action: "unchanged"}, nil
	}
	return writeTracked(path, ".gitignore", existing, withIgnoreEntry(existing), 0o644)
}

// ignoresStateDir reports whether a .gitignore already ignores the Checkout
// Setup, in any of the spellings git accepts.
func ignoresStateDir(existing []byte) bool {
	for _, line := range strings.Split(string(existing), "\n") {
		switch strings.TrimSpace(line) {
		case ".igdev", ".igdev/", "/.igdev", "/.igdev/":
			return true
		}
	}
	return false
}

// withIgnoreEntry appends the managed block, keeping the file's own last line
// intact and separating the block with a blank line.
func withIgnoreEntry(existing []byte) []byte {
	out := append([]byte(nil), existing...)
	if len(out) > 0 {
		if !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
		if !bytes.HasSuffix(out, []byte("\n\n")) {
			out = append(out, '\n')
		}
	}
	out = append(out, gitignoreMark+"\n"...)
	out = append(out, gitignoreEntry+"\n"...)
	return out
}

// printInit shows a human what changed: the diffs on stderr, so a pipe never
// receives prose, and a one-line summary per file on stdout.
func (a *App) printInit(data initData) {
	for _, file := range []initFile{data.Contract, data.Gitignore} {
		if file.Diff != "" {
			fmt.Fprint(a.Stderr, file.Diff)
		}
	}
	fmt.Fprintf(a.Stdout, "contract:   %s (%s, digest %s)\n", data.Contract.Path, data.Contract.Action, data.Contract.Digest)
	fmt.Fprintf(a.Stdout, "gitignore:  %s (%s)\n", data.Gitignore.Path, data.Gitignore.Action)
}
