package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
)

// moduleListData is the `data` member of a successful `igdev module list`
// envelope. The key set and order are frozen by the goldens in
// itest/testdata/golden.
type moduleListData struct {
	IgnitionVersion string `json:"ignition_version"`
	// EnabledAll reports the semantics of an empty whitelist: every module
	// loads, which is what the Gateway image does when GATEWAY_MODULES_ENABLED
	// is empty.
	EnabledAll bool `json:"enabled_all"`
	// Whitelist is the contract's [modules].enabled list, in declared order.
	Whitelist []string `json:"whitelist"`
	// BuiltIn and Private are the selected groups; the group a --built-in or
	// --private flag did not select is absent.
	BuiltIn []moduleBuiltInData `json:"built_in,omitempty"`
	Private []modulePrivateData `json:"private,omitempty"`
}

// moduleBuiltInData is one module that ships in the Ignition image.
type moduleBuiltInData struct {
	ID       string `json:"id"`
	Artifact string `json:"artifact"`
	// Enabled reports whether the whitelist selects the module.
	Enabled bool `json:"enabled"`
}

// modulePrivateData is one private module artifact, or one whitelisted module no
// artifact declares.
type modulePrivateData struct {
	// ID is empty for an artifact whose module.xml could not be read; Error then
	// says why.
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Artifact string `json:"artifact"`
	Source   string `json:"source"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// requireData is the `data` member of a successful `igdev module require`
// envelope: one entry per capability asked about, in argument order.
type requireData struct {
	Capabilities []capabilityData `json:"capabilities"`
}

// scanData is the `data` member of a successful `igdev module scan` envelope.
type scanData struct {
	// Checked is how many distinct capabilities the scan resolved.
	Checked int `json:"checked"`
	// Findings are every occurrence, ordered by file, line, and capability.
	Findings []scanFinding `json:"findings"`
}

// scanFinding is one capability occurrence: its resolution plus where it is.
type scanFinding struct {
	capabilityData
	File string `json:"file"`
	Line int    `json:"line"`
}

func (a *App) newModuleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "module",
		Short: "List modules and check what project code requires",
		Long: `module answers what the Ignition runtime can do before a Gateway starts: which
modules this checkout has, and which modules the code it will run requires.

The knowledge is the Effective Catalog — the Core Catalog embedded in this binary
(33 built-in modules, the Gateway-scope system.* functions with their module
mappings, and the 694 REST operations of Ignition 8.3.8) plus this repository's
tracked Project Overlay, declared as ` + "`[catalog].overlay_paths`" + ` in the Project
Contract.

` + "`require`" + ` resolves a capability: a system.* function, a REST request as
` + "`METHOD /data/path`" + ` (the method is optional and case-insensitive) or a bare
` + "`/data/path`" + `, or an explicit ` + "`module:<id>`" + ` / ` + "`com.*`" + ` id. ` + "`scan`" + ` finds those
references in project code and checks each one, reporting file:line.

Whether a required module is satisfied depends on the contract's
` + "`[modules].enabled`" + ` whitelist and on the ` + "`.modl`" + ` artifacts staged in
` + "`.igdev/modules/`" + `. An empty whitelist means no whitelist: every module loads, which
is the Gateway image's own semantics for GATEWAY_MODULES_ENABLED.`,
		Example: `  igdev module list
  igdev module list --private --json
  igdev module require system.tag.readBlocking
  igdev module require 'GET /data/reporting/api/v1/reports/current' system.report.executeReport
  igdev module scan src/main/python`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newModuleListCmd(), a.newModuleRequireCmd(), a.newModuleScanCmd())
	return cmd
}

func (a *App) newModuleListCmd() *cobra.Command {
	var builtInOnly, privateOnly bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the built-in modules, the private artifacts, and the whitelist",
		Long: `list reports three things: every module that ships in the Ignition image and
whether the whitelist selects it, every private ` + "`.modl`" + ` staged in
` + "`.igdev/modules/`" + ` with its module.xml metadata, and the [modules].enabled
whitelist itself.

A private artifact whose module.xml cannot be read is still listed, with status
UNREADABLE: it is in the directory, so hiding it would hide a broken download. A
whitelist entry that is neither built-in nor declared by an artifact is listed as
MISSING-ARTIFACT, which is the state that makes a Gateway refuse to load.

--built-in and --private select one group each; without them both are reported.

Changing the whitelist is ` + "`igdev module enable`" + ` (ticket 13): this command only
reports.`,
		Example: `  igdev module list
  igdev module list --built-in
  igdev module list --private --json`,
		Args: rejectArgs("module list"),
		RunE: func(_ *cobra.Command, _ []string) error {
			if builtInOnly && privateOnly {
				return contract.UsageFault("igdev module list takes --built-in or --private, not both",
					contract.Remediation{Command: "igdev module list", Why: "report both groups"})
			}
			k, err := a.projectKnowledge()
			if err != nil {
				return err
			}
			data := k.listData(!privateOnly, !builtInOnly)
			a.emit(k.res, data, func() { a.printModuleList(data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&builtInOnly, "built-in", false, "report the built-in modules only")
	cmd.Flags().BoolVar(&privateOnly, "private", false, "report the private module artifacts only")
	return cmd
}

func (a *App) newModuleRequireCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "require <capability>...",
		Short: "Check that every required module of a capability is enabled",
		Long: `require resolves each capability and checks the modules it needs against this
checkout. It is the preflight to run before writing or running code that calls
Gateway-scope functions or REST operations.

A capability is a system.* function, a REST request, or a module id:

  system.tag.readBlocking                            platform, needs nothing
  system.report.executeReport                        requires a module
  GET /data/reporting/api/v1/reports/current         REST, method optional
  /data/perspective/api/v1/sessions/                 bare path, any method
  module:com.inductiveautomation.reporting           explicit module

Unknown capabilities fail with IGDEV_E_UNKNOWN_CAPABILITY. A required module the
whitelist does not name fails with IGDEV_E_MODULE_NOT_ENABLED and the enabling
command in Remediation; an enabled module that is neither built-in nor staged
fails with IGDEV_E_MODULE_ARTIFACT_MISSING and the staging command. A capability
whose rows only exist in this repository's Project Overlay resolves against that
overlay, which is how a private endpoint becomes checkable without a new igdev
release.

Every argument is checked, so one run reports every problem it found.`,
		Example: `  igdev module require system.tag.readBlocking
  igdev module require system.report.executeReport
  igdev module require 'get /data/api/v1/gateway-info' --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("module require", "capability",
					"igdev module require system.report.executeReport",
					"name a capability to check before the code that uses it runs")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			k, err := a.projectKnowledge()
			if err != nil {
				return err
			}
			resolutions, fault := k.verifyAll(args)
			if fault != nil {
				// A mixed run is still readable: the capabilities that passed
				// are reported before the fault.
				if !k.res.IsJSON() {
					a.printCapabilityLines(resolutions)
				}
				return fault
			}
			data := requireData{Capabilities: make([]capabilityData, 0, len(resolutions))}
			for _, res := range resolutions {
				data.Capabilities = append(data.Capabilities, capabilityOf(res))
			}
			a.emit(k.res, data, func() { a.printCapabilityLines(resolutions) })
			return nil
		},
	}
}

func (a *App) newModuleScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan [path]...",
		Short: "Find capability references in project code and check them",
		Long: `scan reads project files, finds the capabilities the code intends to use, and
checks each one: every system.* reference including nested namespaces such as
system.report.executeReport, and every REST path.

A directory is walked for .py, .json, .js, .ts, .tsx, .java, .kt, and .sh files; a
file argument is read whatever its extension. Paths are reported as file:line, so
the finding can be fixed.

Without arguments the contract's ` + "`[scan].capabilities`" + ` paths are used, which is
what ` + "`igdev check`" + ` scans too (ticket 14). A path that does not exist is a warning,
not a failure: the scan reports what it could read.

The Bash foundation scanned for the same references; this command keeps its
findings and adds the line numbers.`,
		Example: `  igdev module scan
  igdev module scan src/main/python scripts
  igdev module scan ignition/script-python --json`,

		RunE: func(_ *cobra.Command, args []string) error {
			k, err := a.projectKnowledge()
			if err != nil {
				return err
			}
			paths := args
			if len(paths) == 0 {
				paths = k.doc.Scan.Capabilities
			}
			if len(paths) == 0 {
				return missingArgument("module scan", "path", "igdev module scan src/main/python",
					"name a path to scan, or declare [scan].capabilities in the contract")
			}
			result := catalog.Scan(paths)
			for _, missing := range result.Missing {
				fmt.Fprintf(a.Stderr, "[igdev] WARNING: module scan path missing: %s\n", missing)
			}

			resolved := map[string]catalog.Resolution{}
			var (
				ok          []catalog.Resolution
				failures    []string
				remediation []contract.Remediation
				code        contract.Code
			)
			for _, capability := range result.Capabilities() {
				res, fault := k.eff.Verify(capability, k.set)
				if fault == nil {
					resolved[capability] = res
					ok = append(ok, res)
					continue
				}
				if code == "" {
					code = fault.Code
				}
				remediation = mergeRemediation(remediation, fault.Remediation...)
				for _, finding := range result.Findings {
					if finding.Capability != capability {
						continue
					}
					failures = append(failures, fmt.Sprintf("%s:%d: %s", finding.File, finding.Line, fault.Message))
				}
			}
			if len(failures) > 0 {
				if !k.res.IsJSON() {
					a.printCapabilityLines(ok)
					a.printScanSummary(result)
				}
				return contract.NewFault(code, contract.ExitFailure, strings.Join(failures, "; ")).
					WithRemediation(remediation...)
			}

			data := scanData{
				Checked:  len(result.Capabilities()),
				Findings: make([]scanFinding, 0, len(result.Findings)),
			}
			for _, finding := range result.Findings {
				data.Findings = append(data.Findings, scanFinding{
					capabilityData: capabilityOf(resolved[finding.Capability]),
					File:           finding.File,
					Line:           finding.Line,
				})
			}
			a.emit(k.res, data, func() {
				a.printCapabilityLines(ok)
				a.printScanSummary(result)
			})
			return nil
		},
	}
}

// printScanSummary is the legacy closing line: how many distinct capabilities
// the scan checked.
func (a *App) printScanSummary(result catalog.ScanResult) {
	n := len(result.Capabilities())
	if n > 0 {
		fmt.Fprintf(a.Stdout, "[igdev] Checked %d native/REST capability reference(s)\n", n)
	}
}

// listData assembles the listing for the selected groups.
func (k *knowledge) listData(showBuiltIn, showPrivate bool) moduleListData {
	data := moduleListData{
		IgnitionVersion: k.eff.Version(),
		EnabledAll:      len(k.whitelist) == 0,
		Whitelist:       append([]string{}, k.whitelist...),
	}
	if showBuiltIn {
		data.BuiltIn = k.builtInRows()
	}
	if showPrivate {
		data.Private = k.privateRows()
	}
	return data
}

// builtInRows lists the modules that ship in the image, in catalog order, each
// with whether the whitelist selects it. A whitelisted solution-suite selector is
// appended: the image resolves it at runtime, so it has no catalog row.
func (k *knowledge) builtInRows() []moduleBuiltInData {
	rows := make([]moduleBuiltInData, 0, len(k.eff.BuiltinModules()))
	seen := map[string]bool{}
	for _, module := range k.eff.BuiltinModules() {
		seen[module.ID] = true
		rows = append(rows, moduleBuiltInData{
			ID:       module.ID,
			Artifact: module.Artifact,
			Enabled:  modules.Enabled(k.whitelist, module.ID),
		})
	}
	for _, raw := range k.whitelist {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] || !strings.HasPrefix(id, modules.SuiteSelectorPrefix) {
			continue
		}
		seen[id] = true
		rows = append(rows, moduleBuiltInData{ID: id, Artifact: "<solution-suite-selector>", Enabled: true})
	}
	return rows
}

// privateRows lists the staged artifacts, then the whitelisted modules no
// artifact declares.
func (k *knowledge) privateRows() []modulePrivateData {
	rows := make([]modulePrivateData, 0, len(k.records))
	for _, record := range k.records {
		// An artifact whose module.xml could not be read has no metadata, but it
		// is in the directory: report it rather than hide it.
		name, version := record.Name, record.Version
		if record.ID == "" {
			name, version = "<unknown>", "-"
		}
		rows = append(rows, modulePrivateData{
			ID:       record.ID,
			Name:     name,
			Version:  version,
			Artifact: record.Artifact,
			Source:   record.Source,
			Status:   modules.Status(record, k.whitelist),
			Error:    record.Err,
		})
	}
	for _, id := range modules.Missing(k.whitelist, k.set.Builtin, k.records) {
		rows = append(rows, modulePrivateData{
			ID:       id,
			Name:     "<unknown>",
			Version:  "-",
			Artifact: "<missing>",
			Source:   "-",
			Status:   modules.StatusMissingArtifact,
		})
	}
	return rows
}

// printModuleList reports the listing in the shape the bash foundation printed:
// the built-in group, then the private table.
func (a *App) printModuleList(data moduleListData) {
	fmt.Fprintf(a.Stdout, "ignition:  %s\n", data.IgnitionVersion)
	if data.EnabledAll {
		fmt.Fprint(a.Stdout, "whitelist: none (every module loads)\n")
	} else {
		fmt.Fprintf(a.Stdout, "whitelist: %s\n", strings.Join(data.Whitelist, ", "))
	}
	if data.BuiltIn != nil {
		fmt.Fprint(a.Stdout, "\nBuilt-in modules effective for this environment:\n")
		for _, row := range data.BuiltIn {
			marker := ""
			if !row.Enabled {
				marker = "  (not enabled)"
			}
			fmt.Fprintf(a.Stdout, "  %-62s %s%s\n", row.ID, row.Artifact, marker)
		}
	}
	if data.Private == nil {
		return
	}
	fmt.Fprint(a.Stdout, "\nPrivate / third-party modules:\n")
	if len(data.Private) == 0 {
		fmt.Fprint(a.Stdout, "  none staged (stage one with: igdev module add <file.modl>)\n")
		return
	}
	table := tabwriter.NewWriter(a.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprint(table, "MODULE ID\tNAME\tVERSION\tARTIFACT\tSOURCE\tSTATUS\n")
	for _, row := range data.Private {
		id := row.ID
		if id == "" {
			id = "<unknown>"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", id, row.Name, row.Version, row.Artifact, row.Source, row.Status)
	}
	table.Flush()
}
