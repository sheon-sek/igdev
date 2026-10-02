package itest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/agentskill"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// `agent context` is the one call an agent makes to orient itself. It works in
// every lifecycle state — uninitialized, initialized without setup, stale, and
// current — and always returns a valid envelope, never a fault. These tests
// freeze the envelope as a golden per state and assert the parts an agent acts
// on.

// agentContext decodes the parts of a context envelope the tests reason about.
type agentContext struct {
	ProjectRoot string `json:"project_root"`
	Lifecycle   struct {
		Initialized bool   `json:"initialized"`
		SetupState  string `json:"setup_state"`
		Consent     struct {
			IgnitionEULA      bool `json:"ignition_eula"`
			ModuleLicense     bool `json:"module_license"`
			ModuleCertificate bool `json:"module_certificate"`
		} `json:"consent"`
	} `json:"lifecycle"`
	Versions struct {
		CLI         string `json:"cli"`
		CLIContract string `json:"cli_contract"`
		Ignition    string `json:"ignition"`
		Jython      string `json:"jython"`
	} `json:"versions"`
	Instance *struct {
		ID        string `json:"id"`
		Namespace string `json:"namespace"`
		Ports     struct {
			HTTP  int `json:"http"`
			HTTPS int `json:"https"`
			Debug int `json:"debug"`
		} `json:"ports"`
	} `json:"instance"`
	Gateway *struct {
		Running bool   `json:"running"`
		URL     string `json:"url"`
	} `json:"gateway"`
	Modules struct {
		Count                       int      `json:"count"`
		Staged                      []string `json:"staged"`
		AllowUnsignedModules        bool     `json:"allow_unsigned_modules"`
		AutoAccepted                []string `json:"auto_accepted"`
		RequirePrivateModuleConsent bool     `json:"require_private_module_consent"`
	} `json:"modules"`
	Catalog struct {
		CoreDigest string `json:"core_digest"`
		Overlay    struct {
			Present bool     `json:"present"`
			Digest  string   `json:"digest"`
			Paths   []string `json:"paths"`
		} `json:"overlay"`
	} `json:"catalog"`
	Capabilities struct {
		NativeFunctions int `json:"native_functions"`
		RestOperations  int `json:"rest_operations"`
		OverlayRows     int `json:"overlay_rows"`
	} `json:"capabilities"`
	Commands struct {
		Check    bool `json:"check"`
		Test     bool `json:"test"`
		Build    bool `json:"build"`
		Verify   bool `json:"verify"`
		Gateway  bool `json:"gateway"`
		Module   bool `json:"module"`
		Baseline bool `json:"baseline"`
		CILocal  bool `json:"ci_local"`
	} `json:"commands"`
}

func contextOf(t *testing.T, res testrig.Result) agentContext {
	t.Helper()
	testrig.WantExit(t, res, contract.ExitOK)
	var data agentContext
	testrig.DataOf(t, res.Stdout, &data)
	return data
}

// Outside a Project Root context is still a valid envelope: initialized false,
// setup_state none, no Instance, no Gateway, and no container-engine call. This
// is the safe first call an agent makes in an unknown directory.
func TestAgentContextUninitialized(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("plain")

	res := env.RunIn(dir, "agent", "context", "--json")
	env.Golden(t, "agent_context_uninitialized.json", res.Stdout)
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)

	data := contextOf(t, res)
	if data.Lifecycle.Initialized {
		t.Error("initialized = true outside a Project Root")
	}
	if data.Lifecycle.SetupState != "none" {
		t.Errorf("setup_state = %q, want none", data.Lifecycle.SetupState)
	}
	if data.Instance != nil || data.Gateway != nil {
		t.Errorf("instance/gateway are not null outside a project: %+v %+v", data.Instance, data.Gateway)
	}
	if data.ProjectRoot != "" {
		t.Errorf("project_root = %q, want empty", data.ProjectRoot)
	}
}

// A checkout that is initialized but never set up reports setup_state required,
// and still resolves the versions and the Core Catalog, because those are
// properties of the binary and the contract, not of a materialized checkout.
func TestAgentContextInitializedWithoutSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)

	res := env.RunIn(dir, "agent", "context", "--json")
	env.Golden(t, "agent_context_no_setup.json", res.Stdout)
	env.AssertNoDockerCalls(t)

	data := contextOf(t, res)
	if !data.Lifecycle.Initialized {
		t.Error("initialized = false inside a Project Root")
	}
	if data.Lifecycle.SetupState != "required" {
		t.Errorf("setup_state = %q, want required", data.Lifecycle.SetupState)
	}
	if data.Instance != nil || data.Gateway != nil {
		t.Error("a checkout with no setup record reports an Instance")
	}
	if data.Catalog.CoreDigest == "" {
		t.Error("the Core Catalog digest is empty before setup")
	}
	if !data.Commands.Module || data.Commands.Check {
		t.Errorf("commands = %+v, want the verbs available and no declared stage", data.Commands)
	}
}

// A Setup Stamp that no longer matches the contract reads as stale; the recorded
// Instance would still be reported if the record carried one.
func TestAgentContextStaleSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)
	env.SetupStamp(dir, filepath.Join(dir, project.ContractFile))

	// Move the contract on disk: the stamp's Contract Digest no longer matches.
	env.Write("repo/igdev.toml", testrig.MinimalContract+"# edited after setup\n")

	res := env.RunIn(dir, "agent", "context", "--json")
	env.Golden(t, "agent_context_stale.json", res.Stdout)
	env.AssertNoDockerCalls(t)

	data := contextOf(t, res)
	if data.Lifecycle.SetupState != "stale" {
		t.Errorf("setup_state = %q, want stale", data.Lifecycle.SetupState)
	}
}

// A current checkout reports the recorded Instance identity, its ports, and the
// Gateway's recorded URL. Context reads the engine (compose ps) to learn whether
// the Gateway is running, but never starts anything.
func TestAgentContextCurrentReportsInstance(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	stamp := setupStampOf(t, dir)
	normalizeInstance(t, env, stamp)

	base := env.Snapshot()
	res := env.RunIn(dir, "agent", "context", "--json")
	env.Golden(t, "agent_context_current.json", res.Stdout)
	env.AssertNoLeaksOutside(t, base, dir, env.Home, "state")

	data := contextOf(t, res)
	if data.Lifecycle.SetupState != "current" {
		t.Errorf("setup_state = %q, want current", data.Lifecycle.SetupState)
	}
	if !data.Lifecycle.Consent.IgnitionEULA {
		t.Error("the accepted EULA is not reported as accepted")
	}
	if data.Instance == nil || data.Instance.ID != stamp.InstanceID {
		t.Fatalf("instance = %+v, want %s", data.Instance, stamp.InstanceID)
	}
	if data.Instance.Ports.HTTP != stamp.Ports.HTTP {
		t.Errorf("instance ports = %+v, want the recorded %+v", data.Instance.Ports, stamp.Ports)
	}
	if data.Gateway == nil || data.Gateway.Running {
		t.Fatalf("gateway = %+v, want present and not running", data.Gateway)
	}
	if want := "http://127.0.0.1:" + strconv.Itoa(stamp.Ports.HTTP); data.Gateway.URL != want {
		t.Errorf("gateway url = %q, want %q", data.Gateway.URL, want)
	}

	// Context asked the engine about the project; it did not start anything.
	var sawPs, sawUp bool
	for _, call := range env.DockerCalls(t) {
		argv := call.Argv
		if len(argv) == 0 || argv[0] != "compose" {
			continue
		}
		switch testrig.ComposeVerb(argv[1:]) {
		case "ps":
			sawPs = true
		case "up", "restart":
			sawUp = true
		}
	}
	if !sawPs {
		t.Error("context did not read the engine's compose state")
	}
	if sawUp {
		t.Error("context started the Gateway")
	}
}

// When the Gateway is running, context reports running true with the recorded
// URL — the answer an agent checks before deciding to start one itself.
func TestAgentContextReportsRunningGateway(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	res := env.RunIn(dir, "agent", "context", "--json")
	data := contextOf(t, res)
	if data.Gateway == nil || !data.Gateway.Running {
		t.Fatalf("gateway = %+v, want running", data.Gateway)
	}
}

// Context reports which staged private module ids igdev would accept without a
// human step, and the contract value that withholds them, so an unattended run
// knows which behaviour it gets (ADR 0006).
func TestAgentContextReportsModuleAcceptance(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	path := modl(t, env, "downloads/acme-vision.modl", "<MODL>", moduleXML("com.acme.vision", "Acme Vision", "1.0.0"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", path), contract.ExitOK)

	byDefault := contextOf(t, env.RunIn(dir, "agent", "context", "--json"))
	if got := strings.Join(byDefault.Modules.AutoAccepted, ","); got != "com.acme.vision" {
		t.Errorf("auto_accepted = %q, want the staged module", got)
	}
	if byDefault.Modules.RequirePrivateModuleConsent {
		t.Error("the opt-in is reported as on by default")
	}

	// The strict opt-in: the same staged id is no longer accepted by igdev.
	strict := `schema = 1

[project]
name = "repo"

[modules]
enabled = []
require_private_module_consent = true
`
	root := env.Project("strict", strict)
	testrig.WantExit(t, env.RunIn(root, "setup", "--accept-eula"), contract.ExitOK)
	path = modl(t, env, "strict-downloads/acme-vision.modl", "<MODL_STRICT>", moduleXML("com.acme.vision", "Acme Vision", "1.0.0"))
	testrig.WantExit(t, env.RunIn(root, "module", "add", path), contract.ExitOK)

	gated := contextOf(t, env.RunIn(root, "agent", "context", "--json"))
	if len(gated.Modules.AutoAccepted) != 0 {
		t.Errorf("auto_accepted = %v, want none under the opt-in", gated.Modules.AutoAccepted)
	}
	if !gated.Modules.RequirePrivateModuleConsent {
		t.Error("the opt-in is not reported")
	}
	if got := strings.Join(gated.Modules.Staged, ","); got != "com.acme.vision" {
		t.Errorf("staged = %q, want the staged module reported either way", got)
	}
}

// A fully set-up fixture returns every field filled: declared pipeline stages
// read as available, and a Project Overlay is reported with its digest, paths,
// and row contribution.
func TestAgentContextReportsDeclaredCommandsAndOverlay(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	contract := `schema = 1

[project]
name = "fixture"

[commands]
check = "./gradlew check"
test = "./gradlew test"

[catalog]
overlay_paths = ["catalog/overlay.tsv"]
`
	dir := env.Project("repo", contract)
	env.Write("repo/catalog/overlay.tsv", "# igdev Project Overlay\n# plane: rest\nGET\t/data/acme/api/v1/widgets\tmodule\tcom.acme.widgets\n")

	res := env.RunIn(dir, "agent", "context", "--json")
	data := contextOf(t, res)

	if !data.Commands.Check || !data.Commands.Test {
		t.Errorf("commands = %+v, want the declared check and test stages true", data.Commands)
	}
	if data.Commands.Build {
		t.Errorf("commands.build = true though the contract declares no build stage")
	}
	if !data.Catalog.Overlay.Present {
		t.Fatalf("overlay = %+v, want present", data.Catalog.Overlay)
	}
	if data.Catalog.Overlay.Digest == "" {
		t.Error("the overlay is present but carries no digest")
	}
	if len(data.Catalog.Overlay.Paths) != 1 || data.Catalog.Overlay.Paths[0] != "catalog/overlay.tsv" {
		t.Errorf("overlay paths = %v, want [catalog/overlay.tsv]", data.Catalog.Overlay.Paths)
	}
	if data.Capabilities.OverlayRows != 1 {
		t.Errorf("overlay_rows = %d, want 1", data.Capabilities.OverlayRows)
	}
	if data.Capabilities.RestOperations != 695 {
		t.Errorf("rest_operations = %d, want the 694 core rows plus the 1 overlay row", data.Capabilities.RestOperations)
	}
}

// The context verb never prompts, whatever the terminal is: a bare invocation on
// a TTY renders the human report and exits.
func TestAgentContextNeverPromptsOnTTY(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("plain")

	session := env.StartPTY(testrig.PTYRun{
		Args: []string{"agent", "context"},
		Dir:  dir,
		Env:  []string{"TERM=xterm-256color"},
	})
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	if strings.Contains(res.Screen, "?") {
		t.Errorf("context rendered a prompt on a TTY:\n%s", res.Screen)
	}
	if !strings.Contains(res.Screen, "project:") {
		t.Errorf("context did not render its human report:\n%s", res.Screen)
	}
}

// Skill installation writes the embedded skill, the SKILL.md entry plus its
// references/, globally by default under the machine's HOME, and touches nothing
// else.
func TestAgentSkillInstallGlobal(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")

	res := env.RunIn(dir, "agent", "skill-install", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "agent_skill_install_global.json", res.Stdout)
	res.AssertNoLeaksOutside(t, env.Home)

	path := filepath.Join(env.Home, ".agents", "skills", "igdev", "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the installed skill: %v", err)
	}
	if string(raw) != string(agentskill.Content()) {
		t.Error("the installed skill is not the embedded document")
	}
	if !strings.Contains(string(raw), "version: \""+contract.Version+"\"") {
		t.Errorf("the installed frontmatter does not carry the CLI Contract Version:\n%s", firstLines(string(raw), 5))
	}
	skillDir := filepath.Join(env.Home, ".agents", "skills", "igdev")
	for name, want := range agentskill.Files() {
		got, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("the skill file %s was not installed: %v", name, err)
		} else if string(got) != string(want) {
			t.Errorf("the installed %s is not the embedded file", name)
		}
	}

	var data struct {
		Scope   string `json:"scope"`
		Action  string `json:"action"`
		Version string `json:"version"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Scope != "global" || data.Action != "created" || data.Version != contract.Version {
		t.Errorf("install report = %+v, want global/created/%s", data, contract.Version)
	}
}

// `--scope repo` installs into the repository, so the skill can be committed and
// reviewed with the project it teaches.
func TestAgentSkillInstallRepoScope(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	res := env.RunIn(dir, "agent", "skill-install", "--scope", "repo", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, dir)

	path := filepath.Join(dir, ".agents", "skills", "igdev", "SKILL.md")
	if raw, err := os.ReadFile(path); err != nil {
		t.Fatalf("read the repo-scoped skill: %v", err)
	} else if string(raw) != string(agentskill.Content()) {
		t.Error("the repo-scoped skill is not the embedded document")
	}
}

// Re-installing an up-to-date skill writes nothing and leaves the file's bytes
// alone; an install left by an older binary is updated in place.
func TestAgentSkillInstallIsIdempotentAndUpdates(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")
	testrig.WantExit(t, env.RunIn(dir, "agent", "skill-install"), contract.ExitOK)
	base := env.Snapshot()

	res := env.RunIn(dir, "agent", "skill-install", "--json")
	var data struct {
		Action string `json:"action"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Action != "unchanged" {
		t.Errorf("re-install action = %q, want unchanged", data.Action)
	}
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("an up-to-date re-install changed %v", changes)
	}

	// An older install (different bytes, an older contract version) is updated.
	path := filepath.Join(env.Home, ".agents", "skills", "igdev", "SKILL.md")
	env.Write("home/.agents/skills/igdev/SKILL.md", "---\nname: igdev\nversion: \"0\"\n---\nstale\n")
	updated := env.RunIn(dir, "agent", "skill-install", "--json")
	testrig.WantExit(t, updated, contract.ExitOK)
	testrig.DataOf(t, updated.Stdout, &data)
	if data.Action != "updated" {
		t.Errorf("stale install action = %q, want updated", data.Action)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the updated skill: %v", err)
	}
	if string(raw) != string(agentskill.Content()) {
		t.Error("the updated skill is not the embedded document")
	}

	// igdev owns SKILL.md and references/: a page an older binary wrote, and
	// the references/ directory it leaves empty, are removed, and removing
	// them is an update. Files beside them belong to the person and survive.
	env.Write("home/.agents/skills/igdev/references/retired.md", "old page\n")
	env.Write("home/.agents/skills/igdev/references/old/gone.md", "old page\n")
	env.Write("home/.agents/skills/igdev/NOTES.md", "my notes\n")
	env.Write("home/.agents/skills/igdev/old/gone.md", "my page\n")
	pruned := env.RunIn(dir, "agent", "skill-install", "--json")
	testrig.WantExit(t, pruned, contract.ExitOK)
	testrig.DataOf(t, pruned.Stdout, &data)
	if data.Action != "updated" {
		t.Errorf("prune action = %q, want updated", data.Action)
	}
	skillDir := filepath.Join(env.Home, ".agents", "skills", "igdev")
	for _, stale := range []string{"references/retired.md", "references/old"} {
		if _, err := os.Lstat(filepath.Join(skillDir, stale)); !os.IsNotExist(err) {
			t.Errorf("the stale %s survived the install (err = %v)", stale, err)
		}
	}
	for _, kept := range []string{"NOTES.md", "old/gone.md", "references/errors.md"} {
		if _, err := os.Stat(filepath.Join(skillDir, kept)); err != nil {
			t.Errorf("the install removed %s: %v", kept, err)
		}
	}
}

// A skill directory that is a symlink (a dotfiles setup) survives the install:
// the link stays a link, and the skill is written through it.
func TestAgentSkillInstallThroughSymlinkedDir(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")
	target := env.Mkdir("dotfiles/igdev")
	link := filepath.Join(env.Home, ".agents", "skills", "igdev")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"created", "unchanged"} {
		res := env.RunIn(dir, "agent", "skill-install", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		var data struct {
			Action string `json:"action"`
		}
		testrig.DataOf(t, res.Stdout, &data)
		if data.Action != want {
			t.Errorf("install action = %q, want %s", data.Action, want)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlinked skill directory did not survive the install (err = %v)", err)
		}
		for name := range agentskill.Files() {
			if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(name))); err != nil {
				t.Errorf("the skill file %s was not written through the link: %v", name, err)
			}
		}
	}
}

// A references/ directory that is a symlink survives the install and is
// populated and pruned through the link.
func TestAgentSkillInstallThroughSymlinkedReferences(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")
	target := env.Mkdir("dotfiles/references")
	env.Write("dotfiles/references/retired.md", "old page\n")
	skillDir := filepath.Join(env.Home, ".agents", "skills", "igdev")
	link := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res := env.RunIn(dir, "agent", "skill-install", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlinked references/ did not survive the install (err = %v)", err)
	}
	for name := range agentskill.Files() {
		rel, ok := strings.CutPrefix(name, "references/")
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the reference %s was not written through the link: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "retired.md")); !os.IsNotExist(err) {
		t.Errorf("the stale retired.md survived the install (err = %v)", err)
	}
}

// `--scope repo` outside a Project Root has nowhere to install, and the fault
// names the repair.
func TestAgentSkillInstallRepoOutsideProject(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")

	res := env.RunIn(dir, "agent", "skill-install", "--scope", "repo", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeNotInitialized)
	testrig.WantRemediation(t, envelope, "igdev init")
}

// An unknown scope is part of the invocation, so it is a usage error naming the
// accepted set, and nothing is written.
func TestAgentSkillInstallRejectsUnknownScope(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")
	base := env.Snapshot()

	res := env.RunIn(dir, "agent", "skill-install", "--scope", "workspace", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUsage)
	if !strings.Contains(envelope.Message, "workspace") {
		t.Errorf("message does not name the rejected scope: %q", envelope.Message)
	}
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a refused run wrote %v", changes)
	}
}

// Installed globally outside a project, skill-install also never prompts on a
// TTY: a bare run reports what it did.
func TestAgentSkillInstallNeverPromptsOnTTY(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")

	session := env.StartPTY(testrig.PTYRun{
		Args: []string{"agent", "skill-install"},
		Dir:  dir,
		Env:  []string{"TERM=xterm-256color"},
	})
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	if strings.Contains(res.Screen, "?") {
		t.Errorf("skill-install rendered a prompt on a TTY:\n%s", res.Screen)
	}
	if !strings.Contains(res.Screen, "skill:") {
		t.Errorf("skill-install did not report the install:\n%s", res.Screen)
	}
}
