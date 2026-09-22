package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/jython"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The frozen stage vocabulary of the check pipeline. Each stage reports one of
// these; the machine contract freezes the words.
const (
	stagePassed  = "passed"
	stageSkipped = "skipped"
	stageFailed  = "failed"
)

// verifyGatewayWait is how long `verify --gateway` gives a starting Gateway: the
// bash specification's `gateway wait 240`.
const verifyGatewayWait = 240 * time.Second

// stageResult is one stage of a pipeline run, as reported. The fields are shared
// because the stage vocabulary is, and each stage fills the ones its kind has.
type stageResult struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	// Message explains a skip, or repeats a stage's own failure detail.
	Message string `json:"message,omitempty"`

	// Enabled and Missing are the module-validate detail: the whitelist, and the
	// ids in it nothing can load.
	Enabled []string `json:"enabled,omitempty"`
	Missing []string `json:"missing,omitempty"`

	// Paths, Checked, and Findings are the module-scan and jython-check detail.
	Paths    []string `json:"paths,omitempty"`
	Checked  int      `json:"checked,omitempty"`
	Findings int      `json:"findings,omitempty"`

	// Command and Exit are the declared-stage detail. Exit is the stage's own
	// exit code, which is also the process exit level when it fails.
	Command string `json:"command,omitempty"`
	Exit    int    `json:"exit,omitempty"`

	// Jar, FileCount, and Diagnostics are the Jython detail. Diagnostics is the
	// structured form of the path:line failures.
	Jar         string              `json:"jar,omitempty"`
	FileCount   int                 `json:"file_count,omitempty"`
	Diagnostics []jython.Diagnostic `json:"diagnostics,omitempty"`

	// ModulesDir, Staged, and RuntimeDir are the re-staging detail.
	ModulesDir string   `json:"modules_dir,omitempty"`
	Staged     []string `json:"staged,omitempty"`
	RuntimeDir string   `json:"runtime_dir,omitempty"`
}

// pipelineData is the `data` member of check, test, build, and verify: the stages
// that ran, in order, and which one stopped the run.
type pipelineData struct {
	Stages []stageResult `json:"stages"`
	// Failed names the stage that stopped the pipeline; empty on success.
	Failed string `json:"failed,omitempty"`
	// GatewayURL is `verify --gateway`'s running Gateway, left up for smoke
	// testing by hand.
	GatewayURL string `json:"gateway_url,omitempty"`
}

// failPipeline marks the stage that failed and hands its fault the per-stage
// results, so a JSON failure reports exactly how far the run got.
func failPipeline(data *pipelineData, stage string, fault *contract.Fault) *contract.Fault {
	data.Failed = stage
	return fault.WithData(*data)
}

func (a *App) newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Run the fixed preflight pipeline before anything else",
		Long: `check is the fast, fixed gate every change passes: the same stages in the same
order, so feedback means the same thing every time.

  1. module-validate  the enabled modules against what this checkout stages
  2. module-scan      the capability references the code makes, over [scan].capabilities
  3. declared-check   [commands].check, run at the Project Root (skipped when undeclared)
  4. jython-check     every .py file under [scan].jython, compiled in one JVM

The order is a contract. Any stage that fails stops the pipeline there, and the
fault names the stage; with ` + "`--json`" + ` the data member carries one entry per
stage that ran, with the failing one marked failed.

The Jython stage replaces the old one-process-per-file check: the pinned
standalone jar is fetched once into the machine-wide cache (sha256-verified,
lock-guarded) and one JVM compiles every file. A syntax error names the file and
the line.`,
		Example: `  igdev check
  igdev check --json`,
		Args: rejectArgs("check"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, k, err := a.projectStaged()
			if err != nil {
				return err
			}
			data := pipelineData{}
			if fault := a.runCheckStages(found, k, &data); fault != nil {
				return fault
			}
			a.emit(k.res, data, func() { a.printPipeline(data) })
			return nil
		},
	}
}

func (a *App) newTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Run the project's declared test stage",
		Long: `test dispatches [commands].test: the project's own test command, run with bash
at the Project Root. igdev orchestrates; the repository stays the source of truth
for how it is tested.

An undeclared stage is skipped and reported as skipped — never failed — so a
repository that declares no test command still passes the verb. A stage that exits
non-zero propagates its own exit code.`,
		Example: `  igdev test
  igdev test --json`,
		Args: rejectArgs("test"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, k, err := a.projectStaged()
			if err != nil {
				return err
			}
			data := pipelineData{}
			if fault := a.runTestStage(found, k, &data); fault != nil {
				return fault
			}
			a.emit(k.res, data, func() { a.printPipeline(data) })
			return nil
		},
	}
}

func (a *App) newBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Run the project's declared build stage, then re-stage the modules",
		Long: `build dispatches [commands].build at the Project Root, then re-materializes the
module staging the Gateway mounts. The re-staging is why a build is worth running
before ` + "`gateway up`" + `: whatever the project produced is what the next Gateway
launch sees.

An undeclared build stage is skipped and reported as skipped. A stage that exits
non-zero propagates its own exit code and stops the run before anything is
re-staged.`,
		Example: `  igdev build
  igdev build --json`,
		Args: rejectArgs("build"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, k, err := a.projectStaged()
			if err != nil {
				return err
			}
			data := pipelineData{}
			if fault := a.runBuildStages(found, k, &data); fault != nil {
				return fault
			}
			a.emit(k.res, data, func() { a.printPipeline(data) })
			return nil
		},
	}
}

func (a *App) newVerifyCmd() *cobra.Command {
	var gateway bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Run check, test, and build in sequence",
		Long: `verify is the whole local gate in one call: check, then test, then build, each
stopping the run when it fails. It is what "is this checkout good" means.

--gateway adds the runtime half: the Gateway starts, is waited for, and is smoke
checked. It is left running so the URL it reports can be opened by hand; stop it
with ` + "`igdev gateway down`" + `.`,
		Example: `  igdev verify
  igdev verify --gateway
  igdev verify --gateway --json`,
		Args: rejectArgs("verify"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, k, err := a.projectStaged()
			if err != nil {
				return err
			}
			data := pipelineData{}
			if fault := a.runCheckStages(found, k, &data); fault != nil {
				return fault
			}
			if fault := a.runTestStage(found, k, &data); fault != nil {
				return fault
			}
			if fault := a.runBuildStages(found, k, &data); fault != nil {
				return fault
			}
			if gateway {
				if fault := a.runGatewayStages(&data); fault != nil {
					return fault
				}
			}
			a.emit(k.res, data, func() { a.printPipeline(data) })
			return nil
		},
	}
	cmd.Flags().BoolVar(&gateway, "gateway", false,
		"also start the Gateway, wait for it, and smoke check it (leaves it running)")
	return cmd
}

// runCheckStages runs the four frozen check stages in order.
func (a *App) runCheckStages(found project.Found, k *knowledge, data *pipelineData) *contract.Fault {
	stage, fault := a.validateStage(k)
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	stage, fault = a.scanStage(found, k)
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	stage, fault = a.declaredStage(found, "declared-check", "check", k.doc.Commands.Check, k.res.IsJSON())
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	stage, fault = a.jythonStage(found, k)
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	return nil
}

// runTestStage runs the declared test stage.
func (a *App) runTestStage(found project.Found, k *knowledge, data *pipelineData) *contract.Fault {
	stage, fault := a.declaredStage(found, "declared-test", "test", k.doc.Commands.Test, k.res.IsJSON())
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	return nil
}

// runBuildStages runs the declared build stage, then re-stages the modules.
func (a *App) runBuildStages(found project.Found, k *knowledge, data *pipelineData) *contract.Fault {
	stage, fault := a.declaredStage(found, "declared-build", "build", k.doc.Commands.Build, k.res.IsJSON())
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	stage, fault = a.restageStage(found, k)
	data.Stages = append(data.Stages, stage)
	if fault != nil {
		return failPipeline(data, stage.Stage, fault)
	}
	return nil
}

// runGatewayStages starts the Gateway, waits for it, and smoke checks it, leaving
// it running. It is `verify --gateway`, the runtime half the bash
// specification gated behind VERIFY_GATEWAY=1.
func (a *App) runGatewayStages(data *pipelineData) *contract.Fault {
	g, err := a.gatewayContext()
	if err != nil {
		stage := stageResult{Stage: "gateway-up", Status: stageFailed, Message: contract.AsFault(err).Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, contract.AsFault(err))
	}
	if err := g.requireRuntimeFiles(); err != nil {
		stage := stageResult{Stage: "gateway-up", Status: stageFailed, Message: contract.AsFault(err).Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, contract.AsFault(err))
	}
	if _, fault := a.admit(g, false); fault != nil {
		stage := stageResult{Stage: "gateway-up", Status: stageFailed, Message: fault.Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, fault)
	}
	if fault := g.compose.Up(); fault != nil {
		stage := stageResult{Stage: "gateway-up", Status: stageFailed, Message: fault.Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, fault)
	}
	data.Stages = append(data.Stages, stageResult{Stage: "gateway-up", Status: stagePassed})
	data.GatewayURL = g.url()

	if err := a.waitForGateway(g, verifyGatewayWait); err != nil {
		fault := contract.AsFault(err)
		stage := stageResult{Stage: "gateway-wait", Status: stageFailed, Message: fault.Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, fault)
	}
	data.Stages = append(data.Stages, stageResult{Stage: "gateway-wait", Status: stagePassed})

	checks := a.runSmokeChecks(g)
	if failed, ok := failedCheck(checks); ok {
		passed := len(checks) - countFailed(checks)
		fault := contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("gateway smoke failed: %s (%d of %d checks passed)", failed.reason(), passed, len(checks))).
			WithRemediation(
				contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read the Gateway's own log"},
				contract.Remediation{Command: "igdev gateway status --json", Why: "report the compose service state"})
		stage := stageResult{Stage: "gateway-smoke", Status: stageFailed, Message: fault.Message}
		data.Stages = append(data.Stages, stage)
		return failPipeline(data, stage.Stage, fault)
	}
	data.Stages = append(data.Stages, stageResult{Stage: "gateway-smoke", Status: stagePassed})
	return nil
}

// validateStage is the first pipeline stage: the enabled modules against what
// this checkout can actually load. It is the bash specification's
// `module validate`, reported as one stage.
func (a *App) validateStage(k *knowledge) (stageResult, *contract.Fault) {
	stage := stageResult{
		Stage:   "module-validate",
		Status:  stagePassed,
		Enabled: append([]string{}, k.whitelist...),
	}
	missing := modules.Missing(k.whitelist, k.set.Builtin, k.records)
	drivers := missingOpcuaBase(k.whitelist)
	if len(missing) == 0 && len(drivers) == 0 {
		if len(k.whitelist) == 0 {
			stage.Message = "whitelist is empty: every module loads"
		}
		return stage, nil
	}
	stage.Status = stageFailed
	stage.Missing = missing
	problems := make([]string, 0, len(missing)+len(drivers))
	for _, id := range missing {
		problems = append(problems, id+" is enabled but its .modl is missing")
	}
	for _, id := range drivers {
		problems = append(problems, id+" requires "+opcuaBase)
	}
	stage.Message = strings.Join(problems, "; ")
	fault := contract.NewFault(contract.CodeModuleArtifactMissing, contract.ExitFailure,
		strings.Join(problems, "; ")).
		WithRemediation(
			contract.Remediation{Command: "igdev module add <file.modl>", Why: "stage the module artifact the whitelist names"},
			contract.Remediation{Command: "igdev module enable <id>", Why: "whitelist a module whose artifact is staged"})
	return stage, fault
}

// opcuaBase is the module every OPC UA driver module depends on: an Ignition
// rule, not an igdev invention, so the pipeline reports it the same way.
const opcuaBase = "com.inductiveautomation.opcua"

// opcuaDriverPrefix is the vendor prefix of the driver modules that need it.
const opcuaDriverPrefix = "com.inductiveautomation.opcua.drivers."

// missingOpcuaBase lists the enabled driver modules whose base module is not
// enabled. An empty whitelist loads everything, so it has nothing to report.
func missingOpcuaBase(whitelist []string) []string {
	if len(whitelist) == 0 {
		return nil
	}
	var out []string
	for _, raw := range whitelist {
		id := strings.TrimSpace(raw)
		if !strings.HasPrefix(id, opcuaDriverPrefix) || modules.Enabled(whitelist, opcuaBase) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// scanStage is the second pipeline stage: the capability references the project
// makes, checked against the Effective Catalog.
func (a *App) scanStage(found project.Found, k *knowledge) (stageResult, *contract.Fault) {
	paths := contractPaths(found.Root, k.doc.Scan.Capabilities)
	stage := stageResult{Stage: "module-scan", Paths: paths}
	if len(paths) == 0 {
		stage.Status = stageSkipped
		stage.Message = "no [scan].capabilities paths declared"
		return stage, nil
	}
	result, _, fault := k.scanCapabilities(paths)
	for _, missing := range result.Missing {
		fmt.Fprintf(a.Stderr, "[igdev] WARNING: module scan path missing: %s\n", missing)
	}
	stage.Checked = len(result.Capabilities())
	stage.Findings = len(result.Findings)
	if fault != nil {
		stage.Status = stageFailed
		stage.Message = fault.Message
		return stage, fault
	}
	stage.Status = stagePassed
	return stage, nil
}

// jythonStage is the last pipeline stage: every .py file under [scan].jython,
// compiled by one JVM against the pinned standalone checker.
func (a *App) jythonStage(found project.Found, k *knowledge) (stageResult, *contract.Fault) {
	paths := contractPaths(found.Root, k.doc.Scan.Jython)
	stage := stageResult{Stage: "jython-check", Paths: paths}
	if len(paths) == 0 {
		stage.Status = stageSkipped
		stage.Message = "no [scan].jython paths declared"
		return stage, nil
	}
	spec := a.jythonSpec(k.res, jythonVersion(k.doc))
	stage.Jar = spec.JarPath()

	files, missing := pythonFiles(paths)
	for _, path := range missing {
		fmt.Fprintf(a.Stderr, "[igdev] WARNING: jython check path missing: %s\n", path)
	}
	stage.FileCount = len(files)
	if len(files) == 0 {
		stage.Status = stagePassed
		stage.Message = "no .py files found"
		return stage, nil
	}

	jar, fault := jython.Ensure(spec)
	if fault != nil {
		stage.Status = stageFailed
		stage.Message = fault.Message
		return stage, fault
	}
	diagnostics, fault := jython.Compile(spec, jar, files)
	if fault != nil {
		stage.Status = stageFailed
		stage.Message = fault.Message
		stage.Diagnostics = diagnostics
		return stage, fault
	}
	stage.Status = stagePassed
	return stage, nil
}

// declaredStage runs one contract-declared command stage with bash at the Project
// Root. An undeclared stage is skipped, never failed.
func (a *App) declaredStage(found project.Found, name, key, command string, jsonMode bool) (stageResult, *contract.Fault) {
	stage := stageResult{Stage: name, Command: command}
	if strings.TrimSpace(command) == "" {
		stage.Status = stageSkipped
		stage.Message = "no [commands]." + key + " declared"
		return stage, nil
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		stage.Status = stageFailed
		return stage, contract.NewFault(contract.CodeCommandFailed, contract.ExitFailure,
			"running the declared "+key+" stage needs bash: "+err.Error()).WithCause(err)
	}
	cmd := exec.Command(shell, "-lc", command)
	cmd.Dir = found.Root
	// In machine mode stdout is the envelope, so a child's own output is progress
	// and belongs on stderr.
	if jsonMode {
		cmd.Stdout = a.Stderr
	} else {
		cmd.Stdout = a.Stdout
	}
	cmd.Stderr = a.Stderr
	if runErr := cmd.Run(); runErr != nil {
		exit := 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exit = exitErr.ExitCode()
		}
		stage.Status = stageFailed
		stage.Exit = exit
		fault := contract.NewFault(contract.CodeCommandFailed, contract.Exit(exit),
			fmt.Sprintf("the declared %s stage exited with code %d: %s", key, exit, command)).WithCause(runErr)
		return stage, fault
	}
	stage.Status = stagePassed
	return stage, nil
}

// restageStage re-materializes the module staging the Gateway mounts, which is
// what `build` adds after the project's own build command.
func (a *App) restageStage(found project.Found, k *knowledge) (stageResult, *contract.Fault) {
	stage := stageResult{Stage: "module-restage"}
	if _, err := a.restage(found, k.doc); err != nil {
		stage.Status = stageFailed
		fault := contract.AsFault(err)
		stage.Message = fault.Message
		return stage, fault
	}
	records, fault := modules.Scan(modules.Dir(found.Root))
	if fault != nil {
		stage.Status = stageFailed
		stage.Message = fault.Message
		return stage, fault
	}
	stage.Status = stagePassed
	stage.ModulesDir = modules.Dir(found.Root)
	stage.Staged = modules.IDs(records)
	stage.RuntimeDir = setupPaths(found.Root).runtime
	return stage, nil
}

// jythonSpec resolves the Jython checker's cache wiring for this run.
func (a *App) jythonSpec(res *config.Resolution, version string) jython.Spec {
	return jython.Spec{
		Version:   version,
		BaseURL:   res.String("jython.maven_base_url"),
		Digest:    res.String("jython.sha256"),
		CacheRoot: xdg.Resolve().Cache,
	}
}

// jythonVersion is the contract's Jython target, or the embedded default when the
// contract leaves it unset.
func jythonVersion(doc project.Doc) string {
	if doc.Ignition.JythonVersion != "" {
		return doc.Ignition.JythonVersion
	}
	return project.DefaultJythonVersion
}

// contractPaths resolves contract-declared, repository-relative paths against the
// Project Root, so a command run from a subdirectory scans the same files a run
// from the root does.
func contractPaths(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if filepath.IsAbs(path) {
			out = append(out, path)
			continue
		}
		out = append(out, filepath.Join(root, path))
	}
	return out
}

// pythonFiles lists the .py files under paths: a directory is walked, an explicit
// file is taken as given. Paths that do not exist are reported, not fatal — the
// scan stage warns about the same thing.
func pythonFiles(paths []string) (files, missing []string) {
	for _, target := range paths {
		info, err := os.Stat(target)
		if err != nil {
			missing = append(missing, target)
			continue
		}
		if !info.IsDir() {
			files = append(files, target)
			continue
		}
		_ = filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".py") {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files, missing
}

// printPipeline reports one line per stage a human reads.
func (a *App) printPipeline(data pipelineData) {
	width := 0
	for _, stage := range data.Stages {
		if len(stage.Stage) > width {
			width = len(stage.Stage)
		}
	}
	for _, stage := range data.Stages {
		name := stage.Stage + ":"
		if detail := stageDetail(stage); detail != "" {
			fmt.Fprintf(a.Stdout, "%-*s %s  (%s)\n", width+1, name, stage.Status, detail)
			continue
		}
		fmt.Fprintf(a.Stdout, "%-*s %s\n", width+1, name, stage.Status)
	}
	if data.GatewayURL != "" {
		fmt.Fprintf(a.Stdout, "%-*s %s\n", width+1, "gateway:", data.GatewayURL)
	}
}

// stageDetail is the parenthetical a human reads beside a stage's status.
func stageDetail(stage stageResult) string {
	switch stage.Stage {
	case "module-validate":
		if len(stage.Enabled) == 0 {
			return "every module loads"
		}
		return fmt.Sprintf("%d enabled", len(stage.Enabled))
	case "module-scan":
		if stage.Status == stageSkipped {
			return ""
		}
		return fmt.Sprintf("%d checked, %d finding(s)", stage.Checked, stage.Findings)
	case "jython-check":
		if stage.Status == stageSkipped {
			return ""
		}
		return fmt.Sprintf("%d file(s)", stage.FileCount)
	case "module-restage":
		if stage.Status == stageSkipped {
			return ""
		}
		return fmt.Sprintf("%d staged", len(stage.Staged))
	case "declared-check", "declared-test", "declared-build":
		if stage.Status == stageSkipped {
			return stage.Message
		}
		// A declared stage may be several lines; the human line stays one.
		return strings.ReplaceAll(strings.TrimSpace(stage.Command), "\n", "\\n")
	default:
		return stage.Message
	}
}
