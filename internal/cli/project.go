package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/projectzip"
)

// projectImportData is what `igdev project import` reports.
type projectImportData struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Zipped is true when Source was a directory igdev zipped.
	Zipped    bool `json:"zipped"`
	Bytes     int  `json:"bytes"`
	Overwrite bool `json:"overwrite"`
	// Changes are the projects the Gateway reports it changed.
	Changes []string `json:"changes"`
}

// projectExportData is what `igdev project export` reports.
type projectExportData struct {
	Name   string `json:"name"`
	Output string `json:"output"`
	// Unpacked is true when Output is a directory the export was unpacked into,
	// and Files then lists what it holds.
	Unpacked bool     `json:"unpacked"`
	Bytes    int      `json:"bytes"`
	Files    []string `json:"files,omitempty"`
}

func (a *App) newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Import and export Ignition projects on the running Gateway",
		Long: `project moves an Ignition project between a directory (or a zip) and this
Instance's running Gateway, through the Gateway's own import and export endpoints
with the Instance token. The Gateway keeps its other projects and configuration.

Projects are not part of ` + "`[gateway] seed`" + `: a seed applies to a fresh volume only, and
a project under development changes all the time. ` + "`gateway reset`" + ` wipes projects, so
` + "`project export`" + ` first keeps one you want back.`,
		Example: `  igdev project import ./projects/demo
  igdev project import demo.zip --name demo --overwrite
  igdev project export demo --output ./projects/demo`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newProjectImportCmd(), a.newProjectExportCmd())
	return cmd
}

func (a *App) newProjectImportCmd() *cobra.Command {
	var (
		name      string
		overwrite bool
	)
	cmd := &cobra.Command{
		Use:   "import <dir|zip>",
		Short: "Import a project directory or zip into the running Gateway",
		Long: `import posts a project to /data/api/v1/projects/import/<name> as application/zip.

A directory is zipped in memory, deterministically, and has to hold project.json at its
root; a .zip is sent as it is, after the same check. --name defaults to the directory
or file name. A project that already exists is a usage error unless --overwrite
replaces it. The report lists the projects the Gateway says it changed.`,
		Example: `  igdev project import ./projects/demo
  igdev project import ./projects/demo --overwrite --json
  igdev project import export.zip --name demo`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("project import", "dir|zip",
					"igdev project import ./projects/demo", "name the project directory or zip to import")
			}
			if len(args) > 1 {
				return extraArguments("project import", 1, args)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			source := args[0]
			body, zipped, fault := projectArchive(source)
			if fault != nil {
				return fault
			}
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(filepath.Clean(source)), filepath.Ext(source))
				if zipped {
					name = filepath.Base(filepath.Clean(source))
				}
			}
			if fault := checkProjectName(name); fault != nil {
				return fault
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			path := "/data/api/v1/projects/import/" + url.PathEscape(name)
			if overwrite {
				path += "?overwrite=true"
			}
			a.stage("project import: %s as %s (%d bytes)", source, name, len(body))
			status, raw, fault := g.restCall(http.MethodPost, path, bytes.NewReader(body), "application/zip", restAnswerLimit)
			if fault != nil {
				return fault
			}
			if status == http.StatusConflict {
				return contract.UsageFault(fmt.Sprintf("the Gateway already has a project named %s", name),
					contract.Remediation{Command: "igdev project import " + source + " --overwrite", Why: "replace the project on the Gateway"},
					contract.Remediation{Command: "igdev project import " + source + " --name <other>", Why: "import it under another name"})
			}
			if status >= 400 {
				return restStatusFault(http.MethodPost, path, status, raw)
			}
			var answer struct {
				Changes []struct {
					Name string `json:"name"`
				} `json:"changes"`
			}
			_ = json.Unmarshal(raw, &answer)
			data := projectImportData{Name: name, Source: source, Zipped: zipped, Bytes: len(body), Overwrite: overwrite, Changes: []string{}}
			for _, c := range answer.Changes {
				data.Changes = append(data.Changes, c.Name)
			}
			a.emit(g.res, data, func() {
				fmt.Fprintf(a.Stdout, "imported: %s (%d bytes from %s)\n", data.Name, data.Bytes, data.Source)
				fmt.Fprintf(a.Stdout, "changed:  %s\n", strings.Join(data.Changes, ", "))
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name on the Gateway (default: the directory or file name)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace a project of the same name")
	return cmd
}

func (a *App) newProjectExportCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export a project from the running Gateway to a directory or zip",
		Long: `export fetches /data/api/v1/projects/export/<name>.

--output ending in .zip writes the zip as the Gateway sent it. Any other --output is a
directory the project is unpacked into, so it round-trips into git: the directory ends
up holding exactly the exported files, and a file the project no longer has is
removed. That directory has to be absent, empty, or already a project directory (one
holding project.json). Without --output the zip is written to ./<name>.zip.`,
		Example: `  igdev project export demo
  igdev project export demo --output ./projects/demo
  igdev project export demo --output demo.zip --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("project export", "name",
					"igdev project export demo --output ./projects/demo", "name the project to export")
			}
			if len(args) > 1 {
				return extraArguments("project export", 1, args)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if fault := checkProjectName(name); fault != nil {
				return fault
			}
			if output == "" {
				output = name + ".zip"
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			path := "/data/api/v1/projects/export/" + url.PathEscape(name)
			status, raw, fault := g.restCall(http.MethodGet, path, nil, "", projectzip.MaxBytes)
			if fault != nil {
				return fault
			}
			if status == http.StatusNotFound {
				return contract.UsageFault(fmt.Sprintf("the Gateway has no project named %s", name),
					contract.Remediation{Command: "igdev gateway api GET /data/api/v1/projects/names", Why: "list the projects the Gateway has"})
			}
			if status >= 400 {
				return restStatusFault(http.MethodGet, path, status, raw)
			}
			data := projectExportData{Name: name, Output: output, Bytes: len(raw)}
			if strings.EqualFold(filepath.Ext(output), ".zip") {
				if err := projectzip.Check(raw); err != nil {
					return contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
						fmt.Sprintf("the Gateway's export of %s is not a project zip: %v", name, err))
				}
				if err := atomicfile.Write(output, raw, 0o644, 0o755); err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot write %s: %v", output, err)).WithCause(err)
				}
			} else {
				files, err := projectzip.Unpack(raw, output)
				if err != nil {
					return contract.UsageFault(fmt.Sprintf("cannot export %s into %s: %v", name, output, err),
						contract.Remediation{Command: "igdev project export " + name + " --output <new-dir>", Why: "export into an empty or project directory"})
				}
				data.Unpacked, data.Files = true, files
			}
			a.emit(g.res, data, func() {
				fmt.Fprintf(a.Stdout, "exported: %s -> %s (%d bytes)\n", data.Name, data.Output, data.Bytes)
				if data.Unpacked {
					fmt.Fprintf(a.Stdout, "files:    %d\n", len(data.Files))
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "a .zip file, or a directory to unpack into (default ./<name>.zip)")
	return cmd
}

// projectArchive reads what `project import` sends: a directory zipped, or a zip
// checked. Anything else is a usage error.
func projectArchive(source string) ([]byte, bool, *contract.Fault) {
	refuse := func(why string) *contract.Fault {
		return contract.UsageFault(fmt.Sprintf("%s is not a project to import: %s", source, why),
			contract.Remediation{Command: "igdev help project import", Why: "pass a project directory with project.json at its root, or its zip"})
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, false, refuse(err.Error())
	}
	if info.IsDir() {
		body, err := projectzip.Zip(source)
		if err != nil {
			return nil, false, refuse(err.Error())
		}
		return body, true, nil
	}
	if info.Size() > projectzip.MaxBytes {
		return nil, false, refuse(fmt.Sprintf("it is over the %d-byte limit", projectzip.MaxBytes))
	}
	body, err := os.ReadFile(source)
	if err != nil {
		return nil, false, refuse(err.Error())
	}
	if err := projectzip.Check(body); err != nil {
		return nil, false, refuse(err.Error())
	}
	return body, false, nil
}

// checkProjectName refuses a name that cannot be one path segment.
func checkProjectName(name string) *contract.Fault {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\?#") {
		return contract.UsageFault(fmt.Sprintf("%q is not a project name", name),
			contract.Remediation{Command: "igdev help project import", Why: "pass --name with a plain project name"})
	}
	return nil
}
