package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/localconfig"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The Wizards are tested through the same seam as every other command, with a
// pseudo-terminal standing in for a person: the keystrokes are scripted, the step
// sequence is frozen as a golden, and every file a Wizard writes has to be
// byte-identical to the file the equivalent flags write.
//
// The terminal is pinned to xterm-256color so the session renders the same way
// wherever the suite runs. huh takes its line-oriented accessible path on
// TERM=dumb, which is a different renderer of the same values, and pinning the
// terminal keeps this suite out of that decision.

// wizardTranscript renders the step sequence a Wizard showed: the banner igdev
// prints for each step, once, in the order the terminal showed them. The banners
// are igdev's own output, so the transcript is stable across terminal renderers.
func wizardTranscript(screen, verb string) string {
	prefix := "[igdev] " + verb + " wizard "
	var out []string
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == line {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// gradleFixture is a repository that looks like an Ignition module build: a
// Gradle build file is all the init Wizard needs to pre-fill [commands].
func gradleFixture(t *testing.T, env *testrig.Env, rel string) string {
	t.Helper()
	dir := env.Project(rel, "")
	env.Write(filepath.Join(rel, "build.gradle"), "plugins { id 'java' }\n")
	return dir
}

// wizardTerminal is the environment every interactive session runs in.
func wizardEnv(env *testrig.Env) []string {
	return []string{"TERM=xterm-256color"}
}

// The init walkthrough: on a Gradle layout the Wizard offers the stages that
// layout implies, and accepting every default writes exactly what the same flags
// write.
func TestWizardInitWalkthroughOnGradleLayout(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := gradleFixture(t, env, "repo")

	session := env.StartPTY(testrig.PTYRun{Args: []string{"init"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Project stack").Send("\r")
	session.Expect("igdev carries a Core Catalog for it").Send("\r")
	session.Expect("Enabled modules").Send("\r")
	session.Expect("[scan].jython").Send("\r")
	session.Expect("[commands].check").Send("\r")
	session.Expect("[commands].test").Send("\r")
	session.Expect("[commands].build").Send("\r")
	session.Expect("[commands].smoke").Send("\r")
	session.Expect("Allow unsigned modules").Send("\r") // no
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, dir)
	env.Golden(t, "wizard_init_transcript.txt", wizardTranscript(res.Screen, "init"))

	want := readFile(t, filepath.Join(dir, project.ContractFile))
	for _, line := range []string{
		`check = "./gradlew check"`,
		`test = "./gradlew test"`,
		`build = "./gradlew build"`,
	} {
		if !strings.Contains(want, line) {
			t.Errorf("the Wizard did not pre-fill %s:\n%s", line, want)
		}
	}

	// The same answers, given as flags, write the same bytes.
	other := gradleFixture(t, env, "repo-flags")
	testrig.WantExit(t, env.RunIn(other,
		"init",
		"--command-check", "./gradlew check",
		"--command-test", "./gradlew test",
		"--command-build", "./gradlew build"), contract.ExitOK)
	if got := readFile(t, filepath.Join(other, project.ContractFile)); got != want {
		t.Errorf("the Wizard wrote a different contract than the flags:\n--- wizard ---\n%s--- flags ---\n%s", want, got)
	}

	// And so does --yes, which is the same defaults without a person.
	yes := gradleFixture(t, env, "repo-yes")
	testrig.WantExit(t, env.RunIn(yes, "init", "--yes"), contract.ExitOK)
	if got := readFile(t, filepath.Join(yes, project.ContractFile)); got != want {
		t.Errorf("--yes wrote a different contract than the Wizard:\n--- wizard ---\n%s--- yes ---\n%s", want, got)
	}
}

// --yes is the fully-flagged Silent Mode path: same file, same output, no
// prompts, on a terminal or anywhere else.
func TestWizardInitYesEqualsFullyFlaggedSilentMode(t *testing.T) {
	env := testrig.NewEnv(t)
	yes := gradleFixture(t, env, "yes")
	flags := gradleFixture(t, env, "flags")
	env.RegisterReplacement(yes, "<ROOT>")
	env.RegisterReplacement(flags, "<ROOT>")

	session := env.StartPTY(testrig.PTYRun{Args: []string{"init", "--yes"}, Dir: yes, Env: wizardEnv(env)})
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, yes)
	if strings.Contains(res.Screen, "Project stack") {
		t.Errorf("--yes prompted on a terminal:\n%s", res.Screen)
	}

	silent := env.RunIn(flags, "init",
		"--command-check", "./gradlew check",
		"--command-test", "./gradlew test",
		"--command-build", "./gradlew build")
	testrig.WantExit(t, silent, contract.ExitOK)
	silent.AssertNoLeaksOutside(t, flags)

	if got, want := readFile(t, filepath.Join(yes, project.ContractFile)), readFile(t, filepath.Join(flags, project.ContractFile)); got != want {
		t.Errorf("--yes and the flags disagree:\n--- yes ---\n%s--- flags ---\n%s", got, want)
	}
	// The terminal carries both streams, so the comparison is line by line
	// against what the piped run printed: the same summary on stdout and the same
	// diff on stderr, in both dialects.
	screen := env.Normalize(res.Screen)
	for _, stream := range []string{silent.Stdout, silent.Stderr} {
		for _, line := range strings.Split(strings.TrimRight(env.Normalize(stream), "\n"), "\n") {
			if line != "" && !strings.Contains(screen, line) {
				t.Errorf("--yes never printed %q", line)
			}
		}
	}
}

// --json suppresses prompting even on a terminal, and the envelope is the one a
// pipe would have received: nothing about machine output depends on the terminal.
func TestWizardJSONSuppressesPromptingOnTerminal(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := gradleFixture(t, env, "tty")
	plain := gradleFixture(t, env, "plain")
	env.RegisterReplacement(dir, "<ROOT>")
	env.RegisterReplacement(plain, "<ROOT>")

	session := env.StartPTY(testrig.PTYRun{Args: []string{"init", "--json"}, Dir: dir, Env: wizardEnv(env)})
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, dir)
	if strings.Contains(res.Screen, "Project stack") {
		t.Errorf("--json prompted on a terminal:\n%s", res.Screen)
	}

	silent := env.RunIn(plain, "init", "--json")
	testrig.WantExit(t, silent, contract.ExitOK)
	silent.AssertNoLeaksOutside(t, plain)
	if got, want := env.Normalize(strings.TrimRight(res.Screen, "\n")), env.Normalize(strings.TrimRight(silent.Stdout, "\n")); got != want {
		t.Errorf("the terminal envelope differs from the piped one:\n--- tty ---\n%s\n--- pipe ---\n%s", got, want)
	}

	// The machine dialect never consults the layout: the stages the Wizard would
	// have offered stay out of the contract.
	if body := readFile(t, filepath.Join(dir, project.ContractFile)); strings.Contains(body, "gradlew") {
		t.Errorf("--json applied the Wizard's defaults:\n%s", body)
	}
}

// --interactive runs the Wizard even when every value is supplied, and every
// prompt arrives preselected: accepting all of them changes nothing, because the
// defaults ARE the contract's own values.
func TestWizardInteractiveForcesPromptsWithDefaultsPreselected(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := gradleFixture(t, env, "repo")
	// A contract that already states everything the Wizard offers, written by the
	// command itself so the file is in igdev's own canonical form.
	testrig.WantExit(t, env.RunIn(dir, "init",
		"--name", "fixture",
		"--modules", "com.inductiveautomation.vision",
		"--scan-jython", "src", "--scan-capabilities", "src",
		"--command-check", "./gradlew check",
		"--command-test", "./gradlew test",
		"--command-build", "./gradlew build"), contract.ExitOK)
	before := readFile(t, filepath.Join(dir, project.ContractFile))

	session := env.StartPTY(testrig.PTYRun{
		Args: []string{"init", "--interactive"},
		Dir:  dir,
		Env:  wizardEnv(env),
	})
	session.Expect("Project stack").Send("\r")
	session.Expect("igdev carries a Core Catalog for it").Send("\r")
	session.Expect("Enabled modules").Send("\r")
	session.Expect("[scan].jython").Send("\r")
	session.Expect("[commands].check").Send("\r")
	session.Expect("[commands].test").Send("\r")
	session.Expect("[commands].build").Send("\r")
	session.Expect("[commands].smoke").Send("\r")
	session.Expect("Allow unsigned modules").Send("\r") // no
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)

	after := readFile(t, filepath.Join(dir, project.ContractFile))
	if after != before {
		t.Errorf("accepting every preselected default changed the contract:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
	env.Golden(t, "wizard_init_interactive_transcript.txt", wizardTranscript(res.Screen, "init"))
	res.AssertNoLeaksOutside(t, dir)
}

// A Wizard needs a terminal: --interactive without one is a usage error, not a
// hang waiting for an answer that cannot arrive.
func TestWizardInteractiveWithoutTerminalIsUsage(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)

	for _, args := range [][]string{
		{"init", "--interactive"},
		{"setup", "--interactive"},
		{"module", "add", "--interactive"},
	} {
		res := env.RunIn(dir, args...)
		testrig.WantExit(t, res, contract.ExitUsage)
		if !strings.Contains(res.Stderr, "terminal on stdin") {
			t.Errorf("%v: stderr = %q", args, res.Stderr)
		}
	}

	// The two flags contradict each other, and saying so beats silently picking
	// one of them.
	res := env.RunIn(dir, "init", "--interactive", "--yes")
	testrig.WantExit(t, res, contract.ExitUsage)
	if !strings.Contains(res.Stderr, "cannot combine") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	res.AssertNoLeaksOutside(t, dir)
}

// The Consent gate: a missing EULA stops the Wizard at exit level 3 with the
// human command visible, and no answer inside the Wizard writes Consent
// (ADR 0004).
func TestWizardSetupConsentMissingExitsThree(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)

	session := env.StartPTY(testrig.PTYRun{Args: []string{"setup"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Has `igdev setup --accept-eula` been run").Send("n")
	res := session.Wait()

	testrig.WantExit(t, res, contract.ExitHumanAction)
	for _, want := range []string{
		"IGDEV_E_CONSENT_REQUIRED",
		"igdev setup --accept-eula",
		"the Ignition EULA is not accepted",
	} {
		if !strings.Contains(res.Screen, want) {
			t.Errorf("the terminal never showed %q:\n%s", want, res.Screen)
		}
	}
	if accepted, err := consent.Load(consent.Path(filepath.Join(env.Home, ".config", "igdev"))); err != nil {
		t.Fatalf("read the Consent record: %v", err)
	} else if _, ok := accepted.Accepted(consent.EULA); ok {
		t.Error("the Wizard wrote an acceptance a prompt answer cannot give")
	}
	// Nothing was materialized either: the missing term stops setup before it
	// writes anything.
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); !os.IsNotExist(err) {
		t.Errorf("the Consent gate materialized a checkout: %v", err)
	}
	res.AssertNoLeaksOutside(t, dir, filepath.Join(env.Home, ".config"))
}

// The flag the Consent gate names has to unblock the gate that prints it: on a
// fresh machine, `setup --accept-eula` in a terminal records the acceptance
// before the Wizard reads the record, so step 1 reports it and the walkthrough
// proceeds to the password step and completes. This is the T3 cell the matrix
// was missing, and the deadlock issue #26 describes.
func TestWizardSetupAcceptEULAUnblocksConsentGate(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)

	session := env.StartPTY(testrig.PTYRun{Args: []string{"setup", "--accept-eula"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Gateway admin password").Send("\r") // generate
	session.Expect("Baseline backup").Send("\r")        // none
	session.Expect("Gateway HTTP port").Send("\r")      // allocate
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)

	if strings.Contains(res.Screen, "Has `igdev setup --accept-eula` been run") {
		t.Errorf("the Acceptance flag still dead-ends in the Consent gate that names it:\n%s", res.Screen)
	}
	if !strings.Contains(res.Screen, "the Ignition EULA is accepted on this machine") {
		t.Errorf("step 1 never reported the recorded acceptance:\n%s", res.Screen)
	}
	if accepted, err := consent.Load(consent.Path(filepath.Join(env.Home, ".config", "igdev"))); err != nil {
		t.Fatalf("read the Consent record: %v", err)
	} else if _, ok := accepted.Accepted(consent.EULA); !ok {
		t.Error("the Acceptance flag did not record the EULA on this machine")
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir)); err != nil {
		t.Errorf("the walkthrough did not materialize the checkout: %v", err)
	}
	res.AssertNoLeaksOutside(t, dir, filepath.Join(env.Home, ".config"))
}

// The setup Wizard's steps, end to end: the Consent record is already there, so
// the walkthrough answers the heap (reported), the password, the Baseline (left
// empty), and the port pin, and the pin is machine-local state.
func TestWizardSetupWalksEveryStep(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)

	session := env.StartPTY(testrig.PTYRun{Args: []string{"setup", "--interactive"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Gateway admin password").Send("\r") // keep, or generate
	session.Expect("Baseline backup").Send("\r")        // none
	session.Expect("Gateway HTTP port").Send("18080\r")
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "wizard_setup_transcript.txt", wizardTranscript(res.Screen, "setup"))

	if !strings.Contains(res.Screen, "18080") {
		t.Errorf("the summary never named the pinned port:\n%s", res.Screen)
	}
	pinned, err := localconfig.LoadPort([]byte(localCredentials(t, dir)))
	if err != nil {
		t.Fatalf("read the port pin: %v", err)
	}
	if pinned != 18080 {
		t.Errorf("http_port = %d, want 18080", pinned)
	}
	if stamp := setupStampOf(t, dir); stamp.Ports.HTTP != 18080 {
		t.Errorf("the recorded HTTP port is %d, want the pin", stamp.Ports.HTTP)
	}

	// The pin is machine-local and outlives the run: a later setup keeps it
	// without being told.
	testrig.WantExit(t, env.RunIn(dir, "setup", "--json"), contract.ExitOK)
	if stamp := setupStampOf(t, dir); stamp.Ports.HTTP != 18080 {
		t.Errorf("a later setup moved off the pin: %d", stamp.Ports.HTTP)
	}
	// The tracked contract never carries a port.
	if body := readFile(t, filepath.Join(dir, project.ContractFile)); strings.Contains(body, "18080") {
		t.Errorf("the port pin reached the tracked contract:\n%s", body)
	}
	res.AssertNoLeaksOutside(t, dir, filepath.Join(env.Home, ".config"))
}

// A Baseline named in the Wizard is staged by the run, so the summary and
// `baseline status` agree about what the next fresh launch restores from.
func TestWizardSetupStagesBaseline(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	backup := env.Write("downloads/customer.gwbk", "GWBK")

	session := env.StartPTY(testrig.PTYRun{Args: []string{"setup", "--interactive"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Gateway admin password").Send("\r")
	session.Expect("Baseline backup").Send(backup + "\r")
	session.Expect("Gateway HTTP port").Send("\r")
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, dir, filepath.Join(env.Home, ".config"))

	staged := filepath.Join(dir, project.StateDir, "baseline", "restore.gwbk")
	if got := readFile(t, staged); got != "GWBK" {
		t.Errorf("the staged Baseline holds %q", got)
	}
	status := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, status, contract.ExitOK)
	if !strings.Contains(status.Stdout, staged) {
		t.Errorf("baseline status does not report the staged file:\n%s", status.Stdout)
	}
}

// The `module add` Wizard: with no argument, a terminal is asked for the archive,
// shown what it declares, and asked to confirm the copy. It never offers the
// whitelist, because a staged module is enabled by being staged (issue #34).
func TestWizardModuleAddStagesTheArtifact(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	source := modl(t, env, "downloads/acme-vision.modl", "<DOWNLOAD>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))

	session := env.StartPTY(testrig.PTYRun{Args: []string{"module", "add"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Module archive").Send(source + "\r")
	session.Expect("Acme Vision").Expect("Copy acme-vision.modl").Send("\r")
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "wizard_module_add_transcript.txt", wizardTranscript(res.Screen, "module add"))

	staged := filepath.Join(dir, project.StateDir, modules.DirName, "acme-vision.modl")
	if got := readFile(t, staged); got != readFile(t, source) {
		t.Error("the artifact was not staged")
	}
	// The staged id is enabled by being staged, so the contract is untouched and
	// the Checkout Setup stays current.
	body := readFile(t, filepath.Join(dir, project.ContractFile))
	if strings.Contains(body, "com.acme.vision") {
		t.Errorf("the Wizard wrote the staged id to the contract:\n%s", body)
	}
	state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout)
	if state.Setup.StampState != "current" {
		t.Errorf("stamp_state = %q, want current: staging is not a contract write", state.Setup.StampState)
	}
	if got := readFile(t, filepath.Join(dir, project.StateDir, "runtime", "compose.env")); !strings.Contains(got, "com.acme.vision") {
		t.Errorf("the rendered module list does not carry the staged module:\n%s", got)
	}
	res.AssertNoLeaksOutside(t, dir)
}

// A declined confirmation stops the run without writing anything: the Wizard's
// answer is a decision, not an error the command invented.
func TestWizardModuleAddDeclinedCopyWritesNothing(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := moduleFixture(t, env, "com.inductiveautomation.perspective")
	source := modl(t, env, "downloads/acme-vision.modl", "<DOWNLOAD>",
		moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))

	session := env.StartPTY(testrig.PTYRun{Args: []string{"module", "add", source, "--interactive"}, Dir: dir, Env: wizardEnv(env)})
	session.Expect("Copy acme-vision.modl").Send("n")
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitUsage)
	if !strings.Contains(res.Screen, "nothing was written") {
		t.Errorf("the terminal never said nothing was written:\n%s", res.Screen)
	}
	if _, err := os.Stat(filepath.Join(dir, project.StateDir, modules.DirName, "acme-vision.modl")); !os.IsNotExist(err) {
		t.Errorf("a declined copy staged the artifact: %v", err)
	}
	res.AssertNoLeaksOutside(t, dir)
}

// --yes on setup is the silent path: the same files, the same output, and never a
// prompt, on a terminal or not.
func TestWizardSetupYesEqualsSilentMode(t *testing.T) {
	env := testrig.NewEnv(t)
	yes := env.Project("yes", testrig.MinimalContract)
	plain := env.Project("plain", testrig.MinimalContract)
	env.RegisterReplacement(yes, "<ROOT>")
	env.RegisterReplacement(plain, "<ROOT>")
	// Accept the EULA once on this machine, so both runs meet the same Consent
	// record and the comparison is about setup, not about who accepted first.
	testrig.WantExit(t, env.RunIn(env.Project("seed", testrig.MinimalContract), "setup", "--accept-eula"), contract.ExitOK)

	session := env.StartPTY(testrig.PTYRun{Args: []string{"setup", "--yes", "--accept-eula"}, Dir: yes, Env: wizardEnv(env)})
	res := session.Wait()
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, yes, filepath.Join(env.Home, ".config"))
	if strings.Contains(res.Screen, "admin password") {
		t.Errorf("--yes prompted on a terminal:\n%s", res.Screen)
	}
	silent := env.RunIn(plain, "setup", "--accept-eula")
	testrig.WantExit(t, silent, contract.ExitOK)
	silent.AssertNoLeaksOutside(t, plain, filepath.Join(env.Home, ".config"))

	normalize := func(root, text string) string {
		normalizeInstance(t, env, setupStampOf(t, root))
		return env.Normalize(text)
	}
	if got, want := normalize(yes, res.Screen), normalize(plain, silent.Stdout); strings.TrimRight(got, "\n") != strings.TrimRight(want, "\n") {
		t.Errorf("--yes printed:\n%q\n--- silent ---\n%q", got, want)
	}
	if got, want := readFile(t, localCredentialsFile(t, yes)), readFile(t, localCredentialsFile(t, plain)); got == want {
		t.Error("two setups generated the same password, which means the credential is not random")
	}
}

// localCredentialsFile is the checkout-local tier's path.
func localCredentialsFile(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, project.StateDir, project.LocalConfig)
}
