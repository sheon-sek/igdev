package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
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
is the Gateway image's own semantics for GATEWAY_MODULES_ENABLED.

The write verbs change what the next Gateway sees. ` + "`enable`" + ` adds module ids to the
tracked whitelist, which moves the Contract Digest and so makes the Checkout Setup
stale until ` + "`igdev setup`" + ` re-materializes it. ` + "`add`" + ` stages a private ` + "`.modl`" + ` in the
checkout and re-renders the runtime, which the next ` + "`gateway up`" + ` mounts. ` + "`clear`" + `
removes the staged artifacts, and ` + "`cache-path`" + ` names the machine-wide cache this
Ignition version shares. Both write verbs pass the Gate, so they need a current
Checkout Setup; every contract write is atomic and prints a unified diff.`,
		Example: `  igdev module list
  igdev module list --private --json
  igdev module require system.tag.readBlocking
  igdev module require 'GET /data/reporting/api/v1/reports/current' system.report.executeReport
  igdev module scan src/main/python
  igdev module enable com.inductiveautomation.perspective
  igdev module add ~/Downloads/com.acme.vision.modl`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		a.newModuleListCmd(),
		a.newModuleRequireCmd(),
		a.newModuleScanCmd(),
		a.newModuleEnableCmd(),
		a.newModuleAddCmd(),
		a.newModuleCachePathCmd(),
		a.newModuleClearCmd(),
	)
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

Changing the whitelist is ` + "`igdev module enable`" + `: this command only reports.`,
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
			result, resolved, fault := k.scanCapabilities(paths)
			for _, missing := range result.Missing {
				fmt.Fprintf(a.Stderr, "[igdev] WARNING: module scan path missing: %s\n", missing)
			}
			if fault != nil {
				if !k.res.IsJSON() {
					a.printCapabilityLines(orderedResolutions(result, resolved))
					a.printScanSummary(result)
				}
				return fault
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
				a.printCapabilityLines(orderedResolutions(result, resolved))
				a.printScanSummary(result)
			})
			return nil
		},
	}
}

// scanCapabilities finds the capability references under paths and checks every
// one against this checkout. It returns the scan, the capabilities that
// resolved, and one fault describing every failure — or a nil fault when all of
// them resolved.
//
// Both `module scan` and the check pipeline's scan stage answer from here, so the
// two report the same findings, the same fault code, and the same remediation.
func (k *knowledge) scanCapabilities(paths []string) (catalog.ScanResult, map[string]catalog.Resolution, *contract.Fault) {
	result := catalog.Scan(paths)
	resolved := map[string]catalog.Resolution{}
	var (
		failures    []string
		remediation []contract.Remediation
		code        contract.Code
	)
	for _, capability := range result.Capabilities() {
		res, fault := k.eff.Verify(capability, k.set)
		if fault == nil {
			resolved[capability] = res
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
		return result, resolved, contract.NewFault(code, contract.ExitFailure, strings.Join(failures, "; ")).
			WithRemediation(remediation...)
	}
	return result, resolved, nil
}

// orderedResolutions lists the resolved capabilities in the order the scan found
// them, which is the order a human reads them in.
func orderedResolutions(result catalog.ScanResult, resolved map[string]catalog.Resolution) []catalog.Resolution {
	out := make([]catalog.Resolution, 0, len(resolved))
	for _, capability := range result.Capabilities() {
		if res, ok := resolved[capability]; ok {
			out = append(out, res)
		}
	}
	return out
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

// moduleEnableData is the `data` member of a successful `igdev module enable`
// envelope. The key set and order are frozen by the goldens in
// itest/testdata/golden.
type moduleEnableData struct {
	// Contract is the tracked write: its path, what the write did, the Contract
	// Digest afterwards, and the unified diff a reviewer reads. A run that
	// changed nothing reports the digest of the contract as it stands and an
	// empty diff.
	Contract initFile `json:"contract"`
	// Whitelist is [modules].enabled after the run, in declared order.
	Whitelist []string `json:"whitelist"`
	// Added lists the ids this run appended, in argument order; empty for a no-op.
	Added []string `json:"added"`
	// AlreadyEnabled lists the requested ids the whitelist already named.
	AlreadyEnabled []string `json:"already_enabled"`
	// Unrestricted reports the empty whitelist's semantics: every module already
	// loads, so there was nothing to enable.
	Unrestricted bool `json:"unrestricted"`
	// SetupStale reports that the write moved the Contract Digest, which makes
	// the Checkout Setup stale until `igdev setup` re-materializes it.
	SetupStale bool `json:"setup_stale"`
}

// moduleAddData is the `data` member of a successful `igdev module add`
// envelope: the metadata the artifact declares, where it was staged, and what
// the re-materialized runtime runs from.
type moduleAddData struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Artifact string `json:"artifact"`
	Path     string `json:"path"`
	// Action is created when the artifact is new to the checkout and replaced
	// when it took the place of one staged under the same file name.
	Action string `json:"action"`
	// Bytes is the staged size, not what the archive claims to expand to.
	Bytes int64 `json:"bytes"`
	// ModulesDir is the staging directory the rendered Compose file mounts.
	ModulesDir string `json:"modules_dir"`
	// Staged lists the module ids the checkout stages after the add, and Count
	// is how many artifacts declare them.
	Staged []string `json:"staged"`
	Count  int      `json:"count"`
	// RuntimeDir is the build context the re-rendered runtime files live in.
	RuntimeDir string       `json:"runtime_dir"`
	Runtime    []setupWrite `json:"runtime"`
	// Enable is the whitelist write `--enable` performed, and is absent when the
	// run staged only: every other `add` envelope keeps its frozen key set.
	Enable *moduleEnableData `json:"enable,omitempty"`
	// SetupStale reports that this run moved the Contract Digest, which makes the
	// Checkout Setup stale until `igdev setup` re-materializes it.
	SetupStale bool `json:"setup_stale,omitempty"`
}

// moduleClearData is the `data` member of a successful `igdev module clear`
// envelope.
type moduleClearData struct {
	ModulesDir string `json:"modules_dir"`
	// Removed lists the staged artifact file names this run deleted.
	Removed []string `json:"removed"`
	Count   int      `json:"count"`
	// Staged lists the module ids still staged, which is empty once nothing is.
	Staged []string `json:"staged"`
	// RuntimeDir is the build context the re-rendered runtime files live in.
	RuntimeDir string       `json:"runtime_dir"`
	Runtime    []setupWrite `json:"runtime"`
}

// moduleCacheData is the `data` member of a successful `igdev module
// cache-path` envelope.
type moduleCacheData struct {
	// Path is the machine-wide module cache directory for this Ignition version.
	Path string `json:"path"`
	// IgnitionVersion is the version the path is keyed by.
	IgnitionVersion string `json:"ignition_version"`
	// Exists reports whether the directory is there yet; an absent cache is
	// normal and never an error, because the cache is disposable.
	Exists bool `json:"exists"`
}

func (a *App) newModuleEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>...",
		Short: "Add modules to the contract's [modules].enabled whitelist",
		Long: `enable writes the module ids into ` + "`[modules].enabled`" + ` in the Project Contract.
It is a contract write, so it follows the same rule as ` + "`igdev init`" + `: the file is
rewritten atomically, never in place, and the unified diff is printed so the change
stays reviewable. There is no backup file.

An id has to be one this environment can actually load — a built-in module from the
Core Catalog, a solution-suite selector, or a module a staged ` + "`.modl`" + ` declares — so
the command checks before it writes. An unknown id fails with
IGDEV_E_MODULE_UNKNOWN, names the closest built-in ids, and names the command that
stages a private module. One unknown id out of several refuses the whole run:
nothing is written.

Enabling a module the whitelist already names changes nothing and writes nothing, so
re-running the command is always safe. An empty whitelist means every module loads,
which is the image's own semantics for GATEWAY_MODULES_ENABLED: with nothing
whitelisted there is nothing to add, and the command says so instead of writing a
list that would restrict the Gateway.

The write moves the Contract Digest, so the Checkout Setup becomes stale and the
Gate refuses the next project command until ` + "`igdev setup`" + ` re-materializes the staging
the Gateway mounts. That is the same rule a hand-edit of the contract follows.`,
		Example: `  igdev module enable com.inductiveautomation.perspective
  igdev module enable com.inductiveautomation.opcua com.inductiveautomation.opcua.drivers.modbus
  igdev module enable com.acme.vision --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("module enable", "id",
					"igdev module enable com.inductiveautomation.perspective",
					"name one or more module ids to add to the whitelist")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			found, k, err := a.projectWrite()
			if err != nil {
				return err
			}
			requested := distinctIDs(args)
			if unknown := k.unknown(requested); len(unknown) > 0 {
				return unknownModulesFault(k, unknown)
			}
			data, err := enableModules(found, k, requested)
			if err != nil {
				return err
			}
			a.emit(k.res, data, func() { a.printModuleEnable(data) })
			return nil
		},
	}
}

// enableModules adds the requested ids to the contract's [modules].enabled
// whitelist and reports what the write did. It is the whole of `module enable`'s
// logic, minus printing, and `module add --enable` runs it too, so both verbs
// write the whitelist through one path.
func enableModules(found project.Found, k *knowledge, requested []string) (moduleEnableData, error) {
	doc := k.doc.Filled(project.DefaultDoc())
	contractPath := filepath.Join(found.Root, project.ContractFile)
	data := moduleEnableData{
		Added:          []string{},
		AlreadyEnabled: []string{},
	}
	current := doc.Modules.Enabled
	switch {
	case len(current) == 0:
		// The empty whitelist already loads every module. Writing a list
		// here would restrict the Gateway, which is not what the caller
		// asked for, so the run reports the state instead.
		data.Unrestricted = true
		data.AlreadyEnabled = requested
		data.Whitelist = []string{}
		data.Contract = initFile{
			Path:   contractPath,
			Action: "unchanged",
			Digest: project.Digest(found.ContractTOML),
		}
	default:
		added := make([]string, 0, len(requested))
		for _, id := range requested {
			if modules.Enabled(current, id) {
				data.AlreadyEnabled = append(data.AlreadyEnabled, id)
				continue
			}
			added = append(added, id)
		}
		data.Added = added
		if len(added) == 0 {
			data.Whitelist = append([]string{}, current...)
			data.Contract = initFile{
				Path:   contractPath,
				Action: "unchanged",
				Digest: project.Digest(found.ContractTOML),
			}
			break
		}
		doc.Modules.Enabled = append(append([]string{}, current...), added...)
		if err := doc.Validate(contractPath); err != nil {
			return data, err
		}
		rendered := doc.Render()
		written, err := writeTracked(contractPath, project.ContractFile, found.ContractTOML, rendered, 0o644)
		if err != nil {
			return data, err
		}
		written.Digest = project.Digest(rendered)
		data.Contract = written
		data.Whitelist = append([]string{}, doc.Modules.Enabled...)
		data.SetupStale = true
	}
	return data, nil
}

func (a *App) newModuleAddCmd() *cobra.Command {
	var (
		wizard wizardFlags
		enable bool
	)
	cmd := &cobra.Command{
		Use:   "add <file.modl>",
		Short: "Stage a private module artifact in the checkout",
		Long: `add stages a private ` + "`.modl`" + ` in the Checkout Setup, re-renders the runtime, and
reports the metadata the archive declares: the module id, its name, and its version.

The file has to be a readable zip carrying a ` + "`module.xml`" + `, which is the only thing
igdev reads out of a ` + "`.modl`" + `: the archive is never a source of capability knowledge
(ADR 0005), so an id, a name, and a version is all ` + "`add`" + ` can tell you. The archive is
read under a size cap — 64 MiB of declared expansion, 1 MiB of module.xml, and no
entry stored at an absurd ratio — so a decompression bomb is refused before anything
is decompressed. A file that is not such an archive fails with
IGDEV_E_MODULE_ARCHIVE_INVALID and the reason.

The artifact is copied into ` + "`.igdev/modules/`" + ` under its own file name, atomically,
with no backup file. That directory is what the rendered Compose file mounts, so the
next ` + "`igdev gateway up`" + ` hands the Gateway the new module; nothing about running a
Gateway changes here. Staging does not touch the Project Contract, so the Setup Stamp
stays current — what changed is what is staged, not what the contract asked for. Run
` + "`igdev module enable <id>`" + ` to add the module to the whitelist as well.

Adding an artifact whose file name is already staged replaces it, which is how a
newer build of the same module is staged.

Without --json or --yes, add never prompts an invocation that named its file: an
agent's fully specified invocation is the whole interface. A terminal that names no
file gets the module add Wizard — four steps that ask for the archive, show what it
declares, state what igdev does not verify, confirm the copy, and offer to whitelist
the module. --interactive runs it even when the file was named; --yes takes the same
defaults without asking; on a non-terminal --interactive is a usage error, because a
Wizard has no way to ask.`,
		Example: `  igdev module add ~/Downloads/com.acme.vision.modl
  igdev module add /mnt/vendor/acme-vision-1.2.3.modl --enable --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return extraArguments("module add", 1, args)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var file string
			if len(args) == 1 {
				file = args[0]
			}
			// The artifact comes before the Gate: an invocation that named none
			// gets the same fault in a repository that was never set up as in one
			// that was, which is what an agent's missing-argument handling sees.
			run, err := a.decide("module add", wizard, a.wantsJSON(), file == "")
			if err != nil {
				return err
			}
			file, err = a.setupModuleAddWizard(cmd, run, file)
			if err != nil {
				return err
			}
			if file == "" {
				return missingArgument("module add", "file",
					"igdev module add ~/Downloads/com.acme.vision.modl",
					"name the module archive to stage in this checkout")
			}
			found, k, err := a.projectWrite()
			if err != nil {
				return err
			}
			// Steps 2-4 need the Gate's verdict: they name the checkout the
			// artifact reaches and the whitelist it would join.
			if err := a.moduleAddWizardSteps(cmd, run, file); err != nil {
				return err
			}
			dir := modules.Dir(found.Root)
			staged, fault := modules.Stage(dir, file)
			if fault != nil {
				return fault
			}
			runtime, err := a.restage(found, k.doc)
			if err != nil {
				return err
			}
			records, fault := modules.Scan(dir)
			if fault != nil {
				return fault
			}
			action := "created"
			if staged.Replaced {
				action = "replaced"
			}
			data := moduleAddData{
				ID:         staged.ID,
				Name:       staged.Name,
				Version:    staged.Version,
				Source:     file,
				Artifact:   staged.Artifact,
				Path:       staged.Path,
				Action:     action,
				Bytes:      staged.Bytes,
				ModulesDir: dir,
				Staged:     modules.IDs(records),
				Count:      len(records),
				RuntimeDir: setupPaths(found.Root).runtime,
				Runtime:    runtime,
			}
			// The whitelist write runs after the staging, so the knowledge it
			// checks against includes the artifact that just arrived.
			if enable {
				fresh, updated, err := a.projectWrite()
				if err != nil {
					return err
				}
				enabled, err := enableModules(fresh, updated, []string{staged.ID})
				if err != nil {
					return err
				}
				data.Enable = &enabled
				data.SetupStale = enabled.SetupStale
			}
			a.emit(k.res, data, func() { a.printModuleAdd(data) })
			return nil
		},
	}
	flags := cmd.Flags()
	wizard.register(cmd)
	flags.BoolVar(&enable, "enable", false,
		"also add the staged module to the contract's [modules].enabled whitelist")
	return cmd
}

func (a *App) newModuleCachePathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cache-path",
		Short: "Print the machine-wide module cache directory for this Ignition version",
		Long: `cache-path prints where igdev keeps the module artifacts it downloads once for
the whole machine, keyed by Ignition version. It is the answer to "where would a
shared module live", and it is printed as a bare path in the human dialect so a
script can use it directly.

The cache is disposable: deleting it costs a re-download and nothing else, and the
directory being absent is reported, never failed on. Moving a repository's private
modules into that cache is ` + "`igdev module add --global`" + `, which this release does not
have yet.`,
		Example: `  igdev module cache-path
  igdev module cache-path --json`,
		Args: rejectArgs("module cache-path"),
		RunE: func(_ *cobra.Command, _ []string) error {
			_, res, err := a.gate()
			if err != nil {
				return err
			}
			version := res.String("ignition.version")
			path := moduleCachePath(version)
			_, statErr := os.Stat(path)
			data := moduleCacheData{
				Path:            path,
				IgnitionVersion: version,
				Exists:          statErr == nil,
			}
			a.emit(res, data, func() { fmt.Fprintln(a.Stdout, data.Path) })
			return nil
		},
	}
}

func (a *App) newModuleClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the private modules this checkout stages",
		Long: `clear deletes every ` + "`.modl`" + ` staged in ` + "`.igdev/modules/`" + ` and re-renders the
runtime, so the next Gateway launch mounts an empty staging directory.

Only the checkout's own staging directory is touched: nothing under the Project
Contract, and nothing in the machine-wide cache. The command reports the file names
it removed and the module ids that remain staged, and it does not ask for
confirmation — the artifacts are disposable checkout state, and re-staging one is
` + "`igdev module add`" + `.

Removing the artifacts does not touch the module whitelist: ` + "`[modules].enabled`" + ` still
names the modules, so a ` + "`module list`" + ` shows them as MISSING-ARTIFACT until the ` + "`.modl`" + `
files are staged again.`,
		Example: `  igdev module clear
  igdev module clear --json`,
		Args: rejectArgs("module clear"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, k, err := a.projectWrite()
			if err != nil {
				return err
			}
			dir := modules.Dir(found.Root)
			removed, fault := modules.Clear(dir)
			if fault != nil {
				return fault
			}
			runtime, err := a.restage(found, k.doc)
			if err != nil {
				return err
			}
			records, fault := modules.Scan(dir)
			if fault != nil {
				return fault
			}
			if removed == nil {
				removed = []string{}
			}
			data := moduleClearData{
				ModulesDir: dir,
				Removed:    removed,
				Count:      len(removed),
				Staged:     modules.IDs(records),
				RuntimeDir: setupPaths(found.Root).runtime,
				Runtime:    runtime,
			}
			a.emit(k.res, data, func() { a.printModuleClear(data) })
			return nil
		},
	}
}

// moduleCachePath is the machine-wide module cache directory for one Ignition
// version: the same XDG cache the rest of igdev's downloads use, namespaced by
// version so two Ignition versions never share a module artifact.
func moduleCachePath(version string) string {
	return filepath.Join(xdg.Resolve().Cache, "modules", version)
}

// restage re-renders the Instance runtime after a module write. Staging lives in
// the Checkout Setup, so an artifact added, replaced, or cleared there has to be
// re-rendered into the files the Gateway launch reads.
//
// It deliberately never writes the Setup Stamp: what moved is what is staged, not
// what the Project Contract asked for, so a checkout that was current stays
// current.
func (a *App) restage(found project.Found, doc project.Doc) ([]setupWrite, error) {
	stamp, ok := gate.Decode(found.SetupRaw)
	if !ok {
		// The Gate admits only a current record, so this is unreachable through
		// discovery; failing closed still beats re-rendering from a record igdev
		// cannot read.
		return nil, contract.NewFault(contract.CodeSetupRequired, contract.ExitFailure,
			fmt.Sprintf("the Checkout Setup record %s cannot be read", found.SetupPath)).
			WithRemediation(contract.Remediation{
				Command: "igdev setup",
				Why:     "re-materialize the Checkout Setup",
			})
	}
	return a.materializeRuntime(found, doc.Filled(project.DefaultDoc()), stamp.InstanceID, stamp.Ports)
}

// distinctIDs trims and deduplicates requested module ids, keeping the caller's
// order: an id repeated on the command line is one module.
func distinctIDs(args []string) []string {
	out := make([]string, 0, len(args))
	seen := map[string]bool{}
	for _, raw := range args {
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// unknown lists the requested ids this environment cannot load: neither a
// built-in module nor an id a staged artifact declares.
func (k *knowledge) unknown(ids []string) []string {
	var out []string
	for _, id := range ids {
		if modules.Builtin(k.eff.Builtin, id) || modules.Has(k.records, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// unknownModulesFault is the refusal to whitelist something nothing can load. A
// typo is the common cause, so each unknown id carries the closest built-in ids.
func unknownModulesFault(k *knowledge, unknown []string) *contract.Fault {
	known := make([]string, 0, len(k.eff.BuiltinModules()))
	for _, module := range k.eff.BuiltinModules() {
		known = append(known, module.ID)
	}
	parts := make([]string, 0, len(unknown))
	for _, id := range unknown {
		clause := id + " is neither a built-in module nor declared by a staged .modl"
		if near := modules.Suggest(known, id, modules.SuggestLimit); len(near) > 0 {
			clause += " (closest: " + strings.Join(near, ", ") + ")"
		}
		parts = append(parts, clause)
	}
	message := strings.Join(parts, "; ")
	if len(unknown) > 1 {
		message = fmt.Sprintf("%d module ids cannot be enabled: %s", len(unknown), message)
	}
	return contract.NewFault(contract.CodeModuleUnknown, contract.ExitFailure, message).
		WithRemediation(
			contract.Remediation{
				Command: "igdev module list --built-in",
				Why:     "list the module ids this igdev knows",
			},
			contract.Remediation{
				Command: "igdev module add <file.modl>",
				Why:     "stage a private module so its id can be enabled",
			})
}

// printModuleEnable shows a human the contract diff, the resulting whitelist, and
// the one consequence that needs acting on: the stale checkout.
func (a *App) printModuleEnable(data moduleEnableData) {
	if data.Contract.Diff != "" {
		fmt.Fprint(a.Stderr, data.Contract.Diff)
	}
	fmt.Fprintf(a.Stdout, "contract:  %s (%s, digest %s)\n",
		data.Contract.Path, data.Contract.Action, data.Contract.Digest)
	switch {
	case data.Unrestricted:
		fmt.Fprint(a.Stdout, "modules:   none added (the whitelist is empty: every module loads)\n")
		fmt.Fprint(a.Stdout, "note:      write an explicit whitelist with `igdev init --modules <ids>`\n")
	case len(data.Added) > 0:
		fmt.Fprintf(a.Stdout, "modules:   added %s\n", strings.Join(data.Added, ", "))
		if len(data.AlreadyEnabled) > 0 {
			fmt.Fprintf(a.Stdout, "already:   %s\n", strings.Join(data.AlreadyEnabled, ", "))
		}
	default:
		fmt.Fprintf(a.Stdout, "modules:   none added (already enabled: %s)\n", strings.Join(data.AlreadyEnabled, ", "))
	}
	if data.SetupStale {
		fmt.Fprint(a.Stdout, "setup:     stale (run `igdev setup` to re-materialize the checkout)\n")
	} else {
		fmt.Fprint(a.Stdout, "setup:     current\n")
	}
}

// printModuleAdd shows a human what was staged and what the re-materialized
// runtime now mounts.
func (a *App) printModuleAdd(data moduleAddData) {
	fmt.Fprintf(a.Stdout, "added:     %s %s (%s)\n", data.Name, data.Version, data.ID)
	fmt.Fprintf(a.Stdout, "artifact:  %s (%s, %d bytes)\n", data.Path, data.Action, data.Bytes)
	fmt.Fprintf(a.Stdout, "staged:    %s\n", stagedLine(data.Count, data.ModulesDir))
	fmt.Fprintf(a.Stdout, "runtime:   re-materialized (%s)\n", data.RuntimeDir)
	if data.Enable != nil {
		a.printModuleEnable(*data.Enable)
	}
}

// printModuleClear shows a human what left the staging directory.
func (a *App) printModuleClear(data moduleClearData) {
	fmt.Fprintf(a.Stdout, "cleared:   %d module file(s) from %s\n", data.Count, data.ModulesDir)
	if len(data.Removed) > 0 {
		fmt.Fprintf(a.Stdout, "removed:   %s\n", strings.Join(data.Removed, ", "))
	}
	fmt.Fprintf(a.Stdout, "staged:    %s\n", stagedLine(len(data.Staged), data.ModulesDir))
	fmt.Fprintf(a.Stdout, "runtime:   re-materialized (%s)\n", data.RuntimeDir)
}

// stagedLine renders how much is staged, naming the ids when there are any.
func stagedLine(count int, dir string) string {
	if count == 0 {
		return "none (" + dir + ")"
	}
	return fmt.Sprintf("%d module file(s) in %s", count, dir)
}
