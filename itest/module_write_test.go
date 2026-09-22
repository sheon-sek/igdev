package itest

import (
	"archive/zip"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The module write verbs: `enable` writes the tracked whitelist, `add` stages a
// private artifact and re-renders the runtime the Gateway mounts, `clear` takes
// the staged artifacts away, and `cache-path` names the machine-wide cache. These
// tests assert the whole observable behaviour through seam S1: exit level, the
// JSON envelope, the tracked-file bytes, the filesystem effects, the printed
// diff, and what the container engine is handed on the next `up`.

// moduleContract is a fixture contract with an explicit whitelist. The whitelist
// matters: an empty one means every module loads, so `module enable` only has
// something to add to once the contract names a module.
func moduleContract(enabled ...string) string {
	quoted := make([]string, 0, len(enabled))
	for _, id := range enabled {
		quoted = append(quoted, `"`+id+`"`)
	}
	return `schema = 1

[project]
name = "fixture"

[ignition]
version = "8.3.8"
jython_version = "2.7.4"
edition = "standard"

[modules]
enabled = [` + strings.Join(quoted, ", ") + `]

[scan]
jython = ["src"]
capabilities = ["src"]

[gateway]
memory_mb = 2048
timezone = "UTC"
`
}

// moduleFixture materializes one Instance and returns the Project Root. The
// module write verbs pass the Gate, so they need a current Checkout Setup.
func moduleFixture(t *testing.T, env *testrig.Env, enabled ...string) string {
	t.Helper()
	dir := env.Project("repo", moduleContract(enabled...))
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	return dir
}

// modl writes a `.modl` — a zip carrying module.xml — into the scratch tree and
// returns its absolute path, registered as a token so a golden can be a literal.
// The fixture is synthetic on purpose: building the archive is the only way to
// test the metadata reader without shipping a proprietary module.
func modl(t *testing.T, env *testrig.Env, rel, token, moduleXML string) string {
	t.Helper()
	path := env.Path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	entry, err := writer.Create("module.xml")
	if err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(moduleXML)); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	env.RegisterReplacement(path, token)
	return path
}

// moduleXML is the metadata a synthetic artifact declares.
func moduleXML(id, name, version string) string {
	return fmt.Sprintf(
		`<modules><module><id>%s</id><name>%s</name><version>%s</version><requiredIgnitionVersion>8.3.0</requiredIgnitionVersion></module></modules>`,
		id, name, version)
}

// readFile reads a file the CLI wrote.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// assertNoLitter fails when the run left a backup or temp file behind anywhere in
// the scratch tree. The bash foundation rewrote tracked files with `sed -i.bak`
// and dropped the backup beside it; the atomic writer never may.
func assertNoLitter(t *testing.T, env *testrig.Env) {
	t.Helper()
	for path := range env.Snapshot() {
		base := filepath.Base(path)
		if strings.Contains(base, ".bak") || strings.Contains(base, ".tmp") {
			t.Errorf("the run left %s behind", path)
		}
	}
}

// stagedArtifacts lists the `.modl` file names in a directory.
func stagedArtifacts(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".modl") {
			out = append(out, entry.Name())
		}
	}
	return out
}

// moduleEnableState is the data member of a `module enable` envelope.
type moduleEnableState struct {
	Contract struct {
		Path   string `json:"path"`
		Action string `json:"action"`
		Digest string `json:"digest"`
		Diff   string `json:"diff"`
	} `json:"contract"`
	Whitelist      []string `json:"whitelist"`
	Added          []string `json:"added"`
	AlreadyEnabled []string `json:"already_enabled"`
	Unrestricted   bool     `json:"unrestricted"`
	SetupStale     bool     `json:"setup_stale"`
}

// enableData decodes the data member of a `module enable` envelope.
func enableData(t *testing.T, stdout string) moduleEnableState {
	t.Helper()
	var data moduleEnableState
	testrig.DataOf(t, stdout, &data)
	return data
}

// moduleAddState is the data member of a `module add` envelope.
type moduleAddState struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Source     string   `json:"source"`
	Artifact   string   `json:"artifact"`
	Path       string   `json:"path"`
	Action     string   `json:"action"`
	Bytes      int64    `json:"bytes"`
	ModulesDir string   `json:"modules_dir"`
	Staged     []string `json:"staged"`
	Count      int      `json:"count"`
}

// moduleClearState is the data member of a `module clear` envelope.
type moduleClearState struct {
	ModulesDir string   `json:"modules_dir"`
	Removed    []string `json:"removed"`
	Count      int      `json:"count"`
	Staged     []string `json:"staged"`
}

// enable writes the contract, prints the diff, and leaves the checkout stale
// until setup re-materializes the staging the Gateway mounts.
func TestModuleEnableWritesTheContract(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	before := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout)
	contractPath := filepath.Join(dir, project.ContractFile)
	contractBefore := readFile(t, contractPath)

	res := env.RunIn(dir, "module", "enable", "com.inductiveautomation.opcua")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_enable.txt", res.Stdout)
	env.Golden(t, "module_enable_diff.txt", res.Stderr)
	res.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)
	if info, err := os.Stat(contractPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("contract mode = %v (%v), want 0644", info, err)
	}

	body := readFile(t, contractPath)
	want := `enabled = ["com.inductiveautomation.perspective", "com.inductiveautomation.opcua"]`
	if !strings.Contains(body, want) {
		t.Errorf("contract does not carry %s:\n%s", want, body)
	}
	if body == contractBefore {
		t.Error("the contract is byte-identical after enable")
	}

	// The write moved the Contract Digest, so the Gate now refuses the checkout
	// until setup re-materializes it (acceptance criterion 1).
	moved := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout)
	if moved.Setup.StampState != "stale" {
		t.Errorf("stamp_state = %q, want stale", moved.Setup.StampState)
	}
	if moved.Contract.Digest == before.Contract.Digest {
		t.Errorf("contract digest did not move: %s", moved.Contract.Digest)
	}
	refused := env.RunIn(dir, "module", "clear", "--json")
	testrig.WantExit(t, refused, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, refused.Stdout), contract.CodeSetupStale)

	// Re-setup clears the staleness, and the whitelist is what the image gets.
	testrig.WantExit(t, env.RunIn(dir, "setup", "--json"), contract.ExitOK)
	if after := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); after.Setup.StampState != "current" {
		t.Errorf("stamp_state after re-setup = %q, want current", after.Setup.StampState)
	}

	// The machine dialect of the same write, frozen as its own golden.
	machine := env.Project("machine", moduleContract("com.inductiveautomation.perspective"))
	testrig.WantExit(t, env.RunIn(machine, "setup", "--accept-eula"), contract.ExitOK)
	jsonRes := env.RunIn(machine, "module", "enable", "com.inductiveautomation.opcua", "--json")
	testrig.WantExit(t, jsonRes, contract.ExitOK)
	env.Golden(t, "module_enable.json", jsonRes.Stdout)
	jsonRes.AssertNoLeaksOutside(t, machine)
	env.AssertNoDockerCalls(t)
}

// An id nothing can load is refused by name, with the closest built-in ids as the
// hint, and nothing is written — not even the ids that were valid.
func TestModuleEnableUnknownIDFaults(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	contractPath := filepath.Join(dir, project.ContractFile)
	before := readFile(t, contractPath)

	res := env.RunIn(dir, "module", "enable", "com.inductiveautomation.perspectiv", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "module_enable_unknown.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleUnknown)
	testrig.WantRemediation(t, envelope, "igdev module list --built-in")
	testrig.WantRemediation(t, envelope, "igdev module add <file.modl>")
	if !strings.Contains(envelope.Message, "com.inductiveautomation.perspective") {
		t.Errorf("the fault does not name the closest id: %q", envelope.Message)
	}
	human := env.RunIn(dir, "module", "enable", "com.inductiveautomation.perspectiv")
	testrig.WantExit(t, human, contract.ExitFailure)
	env.Golden(t, "module_enable_unknown.txt", human.Stderr)
	if human.Stdout != "" {
		t.Errorf("a human failure wrote prose to stdout: %q", human.Stdout)
	}
	res.AssertNoLeaksOutside(t, dir)

	// One unknown id refuses the whole run: the valid ids in the same invocation
	// are not written either.
	mixed := env.RunIn(dir, "module", "enable", "com.inductiveautomation.opcua", "com.acme.vision", "--json")
	testrig.WantExit(t, mixed, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, mixed.Stdout), contract.CodeModuleUnknown)
	if after := readFile(t, contractPath); after != before {
		t.Errorf("a refused enable changed the contract:\n%s", after)
	}
	if stamp := setupStampOf(t, dir); stamp.ContractDigest != project.Digest([]byte(before)) {
		t.Error("a refused enable moved the Setup Stamp")
	}
}

// Enabling what is already enabled is a no-op: no write, no diff, and a checkout
// that is still current. An empty whitelist is the same kind of no-op for a
// different reason — every module already loads.
func TestModuleEnableNoopWritesNothing(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	contractPath := filepath.Join(dir, project.ContractFile)
	before := readFile(t, contractPath)
	stampBefore := setupStampOf(t, dir)

	human := env.RunIn(dir, "module", "enable", "com.inductiveautomation.perspective")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "module_enable_noop.txt", human.Stdout)
	if human.Stderr != "" {
		t.Errorf("a no-op printed a diff: %q", human.Stderr)
	}

	res := env.RunIn(dir, "module", "enable", "com.inductiveautomation.perspective", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	data := enableData(t, res.Stdout)
	if len(data.Added) != 0 || len(data.AlreadyEnabled) != 1 || data.Unrestricted || data.SetupStale {
		t.Errorf("no-op reported %+v", data)
	}
	if data.Contract.Diff != "" {
		t.Errorf("no-op printed a diff: %q", data.Contract.Diff)
	}
	if data.Contract.Action != "unchanged" {
		t.Errorf("action = %q, want unchanged", data.Contract.Action)
	}
	if after := readFile(t, contractPath); after != before {
		t.Error("a no-op rewrote the contract")
	}
	if stamp := setupStampOf(t, dir); stamp != stampBefore {
		t.Error("a no-op moved the Setup Stamp")
	}
	if state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); state.Setup.StampState != "current" {
		t.Errorf("stamp_state = %q, want current", state.Setup.StampState)
	}

	// The empty whitelist loads every module, so there is nothing to add to it:
	// writing a list here would restrict the Gateway to one module.
	unrestricted := testrig.NewEnv(t)
	openDir := moduleFixture(t, unrestricted)
	openPath := filepath.Join(openDir, project.ContractFile)
	openBefore := readFile(t, openPath)
	open := unrestricted.RunIn(openDir, "module", "enable", "com.inductiveautomation.opcua")
	testrig.WantExit(t, open, contract.ExitOK)
	unrestricted.Golden(t, "module_enable_unrestricted.txt", open.Stdout)
	openJSON := unrestricted.RunIn(openDir, "module", "enable", "com.inductiveautomation.opcua", "--json")
	testrig.WantExit(t, openJSON, contract.ExitOK)
	if data := enableData(t, openJSON.Stdout); !data.Unrestricted || len(data.Whitelist) != 0 {
		t.Errorf("empty-whitelist enable reported %+v", data)
	}
	if after := readFile(t, openPath); after != openBefore {
		t.Error("enabling against an empty whitelist rewrote the contract")
	}
}

// `add` validates the archive, reports the metadata it declares, stages the file
// in the mount the Compose file names, and leaves the checkout current: staging
// is not a contract change.
func TestModuleAddStagesArtifact(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	stampBefore := setupStampOf(t, dir)
	source := modl(t, env, "downloads/acme-vision.modl", "<DOWNLOAD>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))

	res := env.RunIn(dir, "module", "add", source, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_add.json", res.Stdout)

	var data moduleAddState
	testrig.DataOf(t, res.Stdout, &data)
	staged := filepath.Join(dir, project.StateDir, modules.DirName, "acme-vision.modl")
	if data.ID != "com.acme.vision" || data.Name != "Acme Vision" || data.Version != "1.2.3" {
		t.Errorf("add reported %s / %s / %s", data.ID, data.Name, data.Version)
	}
	if data.Source != source || data.Path != staged || data.Artifact != "acme-vision.modl" {
		t.Errorf("add reported source %q path %q artifact %q, want the staged %s", data.Source, data.Path, data.Artifact, staged)
	}
	if data.Action != "created" {
		t.Errorf("action = %q, want created", data.Action)
	}
	if data.Bytes != int64(len(readFile(t, source))) {
		t.Errorf("add reported %d bytes, want %d", data.Bytes, len(readFile(t, source)))
	}
	if data.Count != 1 || len(data.Staged) != 1 || data.Staged[0] != "com.acme.vision" {
		t.Errorf("staged report = %+v", data)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("the artifact is not staged: %v", err)
	}
	if staged := stagedArtifacts(t, data.ModulesDir); len(staged) != 1 || staged[0] != "acme-vision.modl" {
		t.Errorf("staging directory holds %v", staged)
	}
	// Staging is not a contract change, so the stamp stays where it was.
	if stamp := setupStampOf(t, dir); stamp != stampBefore {
		t.Error("add moved the Setup Stamp")
	}
	if state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); state.Setup.StampState != "current" {
		t.Errorf("stamp_state = %q, want current", state.Setup.StampState)
	} else if state.Modules.Count != 1 || len(state.Modules.Staged) != 1 || state.Modules.Staged[0] != "com.acme.vision" {
		t.Errorf("status reports modules %+v", state.Modules)
	}
	res.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)

	// The staged id is enable-able and require-able: a private module is a
	// first-class module id once its artifact is there.
	stagedEnable := env.RunIn(dir, "module", "enable", "com.acme.vision", "--json")
	testrig.WantExit(t, stagedEnable, contract.ExitOK)
	if data := enableData(t, stagedEnable.Stdout); len(data.Added) != 1 || data.Added[0] != "com.acme.vision" {
		t.Errorf("enabling the staged module reported %+v", data)
	}
	testrig.WantExit(t, env.RunIn(dir, "setup", "--json"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "module", "require", "module:com.acme.vision"), contract.ExitOK)

	// A second artifact, declaring a different module id, is a second module:
	// what is staged is counted by module, not by file.
	second := modl(t, env, "downloads/acme-reports.modl", "<DOWNLOAD2>",
		moduleXML("com.acme.reports", "Acme Reports", "2.0.0"))
	secondRes := env.RunIn(dir, "module", "add", second, "--json")
	testrig.WantExit(t, secondRes, contract.ExitOK)
	var secondData moduleAddState
	testrig.DataOf(t, secondRes.Stdout, &secondData)
	if secondData.Action != "created" || secondData.Count != 2 {
		t.Errorf("a second module reported %+v, want two staged modules", secondData)
	}

	// Re-adding a file name that is already staged replaces it, which is how a
	// newer build of the same module is staged.
	overwrite := env.RunIn(dir, "module", "add", source, "--json")
	testrig.WantExit(t, overwrite, contract.ExitOK)
	env.Golden(t, "module_add_replaced.json", overwrite.Stdout)
	var replaced moduleAddState
	testrig.DataOf(t, overwrite.Stdout, &replaced)
	if replaced.Action != "replaced" {
		t.Errorf("re-adding the same file name reported %q, want replaced", replaced.Action)
	}
	env.AssertNoDockerCalls(t)
}

// What `add` stages is what the next `gateway up` mounts: the artifact is in the
// directory the recorded Compose file names, and the recorded environment file
// points at the same directory. This is the wiring, asserted through the shim.
func TestModuleAddStagingReachesGatewayUp(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(16384)
	dir := moduleFixture(t, env)
	source := modl(t, env, "downloads/acme-vision.modl", "<DOWNLOAD>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", source), contract.ExitOK)

	up := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, up, contract.ExitOK)

	composeFile := ""
	envFile := ""
	for _, call := range env.DockerCalls(t) {
		argv := call.Argv
		if len(argv) > 0 && filepath.Base(argv[0]) == "docker" {
			argv = argv[1:]
		}
		if len(argv) == 0 || argv[0] != "compose" || testrig.ComposeVerb(argv[1:]) != "up" {
			continue
		}
		// The compose invocation names the file long-form; -f is accepted too,
		// because the shim records whatever the engine was handed.
		composeFile = argvFlagValue(argv, "--file")
		if composeFile == "" {
			composeFile = argvFlagValue(argv, "-f")
		}
		envFile = argvFlagValue(argv, "--env-file")
	}
	if composeFile == "" {
		t.Fatal("the shim recorded no compose up call naming a compose file")
	}
	rendered := readFile(t, composeFile)
	mount := ""
	for _, line := range strings.Split(rendered, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ":/usr/local/bin/ignition/user-lib/modules") {
			mount = strings.TrimPrefix(line, "- ")
			mount = strings.TrimSuffix(mount, ":/usr/local/bin/ignition/user-lib/modules")
		}
	}
	if mount == "" {
		t.Fatalf("the rendered Compose file names no module mount:\n%s", rendered)
	}
	env.RegisterReplacement(mount, "<MODULES>")
	staged := strings.Join(stagedArtifacts(t, mount), ", ")
	if staged != "acme-vision.modl" {
		t.Errorf("the Gateway would mount %q, want the staged artifact", staged)
	}
	// The environment file the engine was handed points at the same directory, so
	// nothing downstream has to guess where modules live.
	envBody := readFile(t, envFile)
	if !strings.Contains(envBody, "IGDEV_MODULES_PATH="+mount) {
		t.Errorf("the Compose environment does not name %s:\n%s", mount, envBody)
	}
	env.Golden(t, "module_add_gateway.txt", fmt.Sprintf(
		"mount:  %s\nenv:    IGDEV_MODULES_PATH=%s\nstaged: %s\n", mount, mount, staged))
	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes", "--json"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// `clear` removes what is staged without asking, re-renders the runtime, and
// leaves the whitelist alone — so a whitelisted module whose artifact went away
// reads as MISSING-ARTIFACT, which is the state that makes a Gateway refuse it.
func TestModuleClearRemovesStagedArtifacts(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.acme.vision")
	for _, name := range []string{"acme-vision.modl", "acme-reports.modl"} {
		source := modl(t, env, "downloads/"+name, "<DOWNLOAD-"+name+">",
			moduleXML("com.acme."+strings.TrimSuffix(strings.TrimPrefix(name, "acme-"), ".modl"), "Acme", "1.0.0"))
		testrig.WantExit(t, env.RunIn(dir, "module", "add", source), contract.ExitOK)
	}
	stampBefore := setupStampOf(t, dir)

	res := env.RunIn(dir, "module", "clear", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_clear.json", res.Stdout)
	var data moduleClearState
	testrig.DataOf(t, res.Stdout, &data)
	if data.Count != 2 || len(data.Removed) != 2 || len(data.Staged) != 0 {
		t.Errorf("clear reported %+v", data)
	}
	if staged := stagedArtifacts(t, data.ModulesDir); len(staged) != 0 {
		t.Errorf("clear left %v staged", staged)
	}
	if stamp := setupStampOf(t, dir); stamp != stampBefore {
		t.Error("clear moved the Setup Stamp")
	}
	if state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); state.Modules.Count != 0 {
		t.Errorf("status still counts %d staged module(s)", state.Modules.Count)
	}
	res.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)

	// The whitelist survives: the module is now whitelisted with nothing to load.
	listing := env.RunIn(dir, "module", "list", "--private")
	testrig.WantExit(t, listing, contract.ExitOK)
	if !strings.Contains(listing.Stdout, modules.StatusMissingArtifact) {
		t.Errorf("the listing does not report the vanished artifact:\n%s", listing.Stdout)
	}

	// Clearing an empty staging directory is a no-op, not a failure.
	empty := env.RunIn(dir, "module", "clear")
	testrig.WantExit(t, empty, contract.ExitOK)
	env.Golden(t, "module_clear_empty.txt", empty.Stdout)
	env.AssertNoDockerCalls(t)
}

// `cache-path` answers where the machine-wide cache for this Ignition version
// lives. It is a property of the machine and the version, so it works outside a
// Project Root too, and it is printed as a bare path a script can use.
func TestModuleCachePath(t *testing.T) {
	env := testrig.NewEnv(t)
	outside := env.RunIn(env.Home, "module", "cache-path", "--json")
	testrig.WantExit(t, outside, contract.ExitOK)
	env.Golden(t, "module_cache_path.json", outside.Stdout)
	outside.AssertNoLeaks(t)

	human := env.RunIn(env.Home, "module", "cache-path")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "module_cache_path.txt", human.Stdout)
	human.AssertNoLeaks(t)
	want := filepath.Join(env.Home, ".cache", "igdev", "modules", "8.3.8")
	if strings.TrimSpace(human.Stdout) != want {
		t.Errorf("cache-path printed %q, want the bare path %q", human.Stdout, want)
	}

	// Inside a contract that targets another version, the path is keyed by that
	// version: two Ignitions never share a module cache.
	dir := env.Project("other", strings.Replace(moduleContract(), `version = "8.3.8"`, `version = "8.1.21"`, 1))
	res := env.RunIn(dir, "module", "cache-path")
	testrig.WantExit(t, res, contract.ExitOK)
	if want := filepath.Join(env.Home, ".cache", "igdev", "modules", "8.1.21"); strings.TrimSpace(res.Stdout) != want {
		t.Errorf("cache-path printed %q, want %q", res.Stdout, want)
	}
	res.AssertNoLeaks(t)
	env.AssertNoDockerCalls(t)
}

// A file that is not a module archive is refused with the reason, and nothing is
// staged. The zip-bomb guard is one of those reasons: an archive that would
// expand into a small file at an absurd ratio, or past the size limits, never
// gets decompressed.
func TestModuleAddRejectsBadArchives(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env)
	notZip := env.Write("downloads/not-a-zip.modl", "this is not a zip file\n")
	noMetadata := env.Write("downloads/no-module-xml.modl", "")
	zipWithOtherEntry(t, noMetadata)
	noID := modl(t, env, "downloads/no-id.modl", "<NOID>", `<modules><module><name>Nameless</name></module></modules>`)
	// The bomb stays under the metadata size limit and is caught by the ratio it
	// would expand at; the large one is incompressible and caught by size.
	bomb := modlBytes(t, env, "downloads/bomb.modl", "<BOMB>", 512<<10, false)
	large := modlBytes(t, env, "downloads/large-metadata.modl", "<LARGE>", 2<<20, true)

	for _, tc := range []struct {
		name     string
		path     string
		contains string
	}{
		{"not a zip", notZip, "not a valid zip file"},
		{"no module.xml", noMetadata, "carries no module.xml"},
		{"no id", noID, "carries no <id>"},
		{"bomb ratio", bomb, "looks like a decompression bomb"},
		{"metadata too large", large, "past the 1048576-byte metadata limit"},
	} {
		res := env.RunIn(dir, "module", "add", tc.path, "--json")
		testrig.WantExit(t, res, contract.ExitFailure)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeModuleArchiveInvalid)
		testrig.WantRemediation(t, envelope, "igdev help module add")
		if !strings.Contains(envelope.Message, tc.contains) {
			t.Errorf("%s: message %q does not carry %q", tc.name, envelope.Message, tc.contains)
		}
		if tc.name == "bomb ratio" {
			env.Golden(t, "module_add_invalid.json", res.Stdout)
			human := env.RunIn(dir, "module", "add", tc.path)
			testrig.WantExit(t, human, contract.ExitFailure)
			env.Golden(t, "module_add_invalid.txt", human.Stderr)
		}
	}

	// A file name that would not be read back as a module is refused too: staging
	// it would look like a successful add and behave like none, because the
	// reader only ever picks up `.modl` artifacts.
	renamed := modl(t, env, "downloads/acme-vision.zip", "<RENAMED>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	renamedRes := env.RunIn(dir, "module", "add", renamed, "--json")
	testrig.WantExit(t, renamedRes, contract.ExitFailure)
	if envelope := testrig.Envelope(t, renamedRes.Stdout); !strings.Contains(envelope.Message, "does not end in .modl") {
		t.Errorf("the refusal does not name the extension: %q", envelope.Message)
	}

	if staged := stagedArtifacts(t, filepath.Join(dir, project.StateDir, modules.DirName)); len(staged) != 0 {
		t.Errorf("a rejected archive was staged: %v", staged)
	}
	good := modl(t, env, "downloads/acme.modl", "<ACME>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", good), contract.ExitOK)
	env.AssertNoDockerCalls(t)
}

// The write verbs pass the Gate: without a Checkout Setup there is nothing to
// stage into, and outside a Project Root there is no contract to write.
func TestModuleWriteGateAndUsage(t *testing.T) {
	env := testrig.NewEnv(t)
	uninitialized := env.Project("bare", "")
	res := env.RunIn(uninitialized, "module", "enable", "com.inductiveautomation.opcua", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeNotInitialized)

	// A contract without a setup: the repair is named, and nothing is written.
	dir := env.Project("repo", moduleContract("com.inductiveautomation.perspective"))
	before := readFile(t, filepath.Join(dir, project.ContractFile))
	unset := env.RunIn(dir, "module", "enable", "com.inductiveautomation.opcua", "--json")
	testrig.WantExit(t, unset, contract.ExitFailure)
	envelope := testrig.Envelope(t, unset.Stdout)
	testrig.WantCode(t, envelope, contract.CodeSetupRequired)
	testrig.WantRemediation(t, envelope, "igdev setup")
	if after := readFile(t, filepath.Join(dir, project.ContractFile)); after != before {
		t.Error("a refused enable wrote the contract")
	}
	add := env.RunIn(dir, "module", "add", "/tmp/nothing.modl", "--json")
	testrig.WantExit(t, add, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, add.Stdout), contract.CodeSetupRequired)

	for _, tc := range []struct {
		args []string
		want contract.Code
	}{
		{[]string{"module", "enable"}, contract.CodeMissingArgument},
		{[]string{"module", "add"}, contract.CodeMissingArgument},
		{[]string{"module", "add", "one.modl", "two.modl"}, contract.CodeUsage},
		{[]string{"module", "clear", "extra"}, contract.CodeUsage},
		{[]string{"module", "cache-path", "extra"}, contract.CodeUsage},
	} {
		res := env.RunIn(dir, append(tc.args, "--json")...)
		testrig.WantExit(t, res, contract.ExitUsage)
		testrig.WantCode(t, testrig.Envelope(t, res.Stdout), tc.want)
	}
	env.AssertNoDockerCalls(t)
}

// Staging survives a wiped runtime: the module verbs re-render what the Gateway
// launch reads, so a checkout whose generated runtime was deleted is repaired by
// the next staging change instead of by a compose call failing on a missing file.
func TestModuleAddRestoresWipedRuntime(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env)
	runtimeDir := filepath.Join(dir, project.StateDir, "runtime")
	composePath := filepath.Join(runtimeDir, "compose.yaml")
	if err := os.Remove(composePath); err != nil {
		t.Fatalf("remove %s: %v", composePath, err)
	}
	source := modl(t, env, "downloads/acme-vision.modl", "<DOWNLOAD>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", source), contract.ExitOK)
	if _, err := os.Stat(composePath); err != nil {
		t.Errorf("the Compose file was not re-materialized: %v", err)
	}
}

// zipWithOtherEntry writes a readable zip whose only entry is not module.xml.
func zipWithOtherEntry(t *testing.T, path string) {
	t.Helper()
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	if _, err := writer.Create("license.txt"); err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// modlBytes builds a `.modl` whose module.xml entry decompresses to size bytes.
// random fills the entry with incompressible data, which keeps the compression
// ratio near 1 so the size limit is what trips; otherwise a repeated filler
// compresses hard enough to trip the ratio limit first.
func modlBytes(t *testing.T, env *testrig.Env, rel, token string, size int, random bool) string {
	t.Helper()
	payload := make([]byte, size)
	if random {
		if _, err := rand.Read(payload); err != nil {
			t.Fatalf("random payload: %v", err)
		}
	} else {
		for i := range payload {
			payload[i] = 'A'
		}
	}
	path := env.Path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	entry, err := writer.Create("module.xml")
	if err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if _, err := entry.Write([]byte("<module><id>com.acme.bomb</id><name>")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if _, err := entry.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	env.RegisterReplacement(path, token)
	return path
}

// argvFlagValue is the value of `--flag value` or `--flag=value` in a recorded
// engine call.
func argvFlagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value
		}
	}
	return ""
}
