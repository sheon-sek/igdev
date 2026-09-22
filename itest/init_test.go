package itest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// init is the only command that mutates tracked files, so its tests assert the
// whole change set: exactly which files appeared, what they hold, and that a run
// with nothing to say writes nothing.

func TestInitCreatesContractAndGitignore(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	base := env.Snapshot()

	res := env.RunIn(dir, "init", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "init_created.json", res.Stdout)
	env.AssertNoDockerCalls(t)

	if changes := env.Changes(base); len(changes) != 3 {
		t.Errorf("init changed %v, want exactly igdev.toml, .gitignore, and AGENTS.md", changes)
	}
	contractRaw, err := os.ReadFile(filepath.Join(dir, "igdev.toml"))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	for _, want := range []string{
		"schema = 1",
		"[ignition]",
		`version = "8.3.8"`,
		`jython_version = "2.7.4"`,
		`edition = "standard"`,
		`enabled = []`,
		`jython = ["src/main/python"]`,
		"memory_mb = 2048",
		`timezone = "UTC"`,
	} {
		if !strings.Contains(string(contractRaw), want) {
			t.Errorf("contract does not declare %q:\n%s", want, contractRaw)
		}
	}
	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), ".igdev/") {
		t.Errorf(".gitignore does not ignore the Checkout Setup:\n%s", gitignore)
	}
	// init writes the contract; it never materializes a Checkout Setup.
	if _, err := os.Stat(filepath.Join(dir, ".igdev")); !os.IsNotExist(err) {
		t.Errorf("init created .igdev/, which belongs to setup")
	}

	// The JSON payload is the machine-readable record of the same change.
	var data struct {
		Contract struct {
			Path   string `json:"path"`
			Action string `json:"action"`
			Digest string `json:"digest"`
			Diff   string `json:"diff"`
		} `json:"contract"`
		Gitignore struct {
			Path   string `json:"path"`
			Action string `json:"action"`
			Diff   string `json:"diff"`
		} `json:"gitignore"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Contract.Action != "created" || data.Gitignore.Action != "created" {
		t.Errorf("actions = %q/%q, want created/created", data.Contract.Action, data.Gitignore.Action)
	}
	if !strings.HasPrefix(data.Contract.Digest, "sha256:") {
		t.Errorf("digest = %q, want a named sha256", data.Contract.Digest)
	}
	if !strings.Contains(data.Contract.Diff, "+++ b/igdev.toml") {
		t.Errorf("contract diff is not a unified diff:\n%s", data.Contract.Diff)
	}
	if strings.Contains(data.Contract.Diff, ".bak") {
		t.Errorf("diff mentions a backup file:\n%s", data.Contract.Diff)
	}
}

// A second run is an edit: the flag overrides the value it names, everything
// else is preserved, and the diff shows only the change.
func TestInitSecondRunEditsAndPrintsDiff(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	res := env.RunIn(dir, "init", "--json",
		"--ignition-version", "8.1.21",
		"--modules", "com.inductiveautomation.perspective,com.inductiveautomation.opcua",
		"--command-check", "./gradlew check")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "init_edited.json", res.Stdout)

	contractRaw, err := os.ReadFile(filepath.Join(dir, "igdev.toml"))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	for _, want := range []string{
		`version = "8.1.21"`,
		`enabled = ["com.inductiveautomation.perspective", "com.inductiveautomation.opcua"]`,
		`check = "./gradlew check"`,
		// untouched values survive the edit
		`jython_version = "2.7.4"`,
		`timezone = "UTC"`,
	} {
		if !strings.Contains(string(contractRaw), want) {
			t.Errorf("edited contract does not declare %q:\n%s", want, contractRaw)
		}
	}

	var data struct {
		Contract struct {
			Action string `json:"action"`
			Diff   string `json:"diff"`
		} `json:"contract"`
		Gitignore struct {
			Action string `json:"action"`
		} `json:"gitignore"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Contract.Action != "updated" {
		t.Errorf("contract action = %q, want updated", data.Contract.Action)
	}
	if !strings.Contains(data.Contract.Diff, `-version = "8.3.8"`) ||
		!strings.Contains(data.Contract.Diff, `+version = "8.1.21"`) {
		t.Errorf("diff does not show the edit:\n%s", data.Contract.Diff)
	}
	if data.Gitignore.Action != "unchanged" {
		t.Errorf("gitignore action = %q, want unchanged on the second run", data.Gitignore.Action)
	}
}

// In human mode the diff goes to stderr and the summary to stdout, so a pipe
// never receives prose.
func TestInitHumanPrintsDiffOnStderr(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")

	res := env.RunIn(dir, "init")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "init_human.txt", res.Stdout)
	env.Golden(t, "init_human_diff.txt", res.Stderr)
	if !strings.Contains(res.Stderr, "+++ b/igdev.toml") {
		t.Errorf("stderr does not carry the contract diff:\n%s", res.Stderr)
	}
	if strings.Contains(res.Stdout, "+++") {
		t.Errorf("stdout carries the diff:\n%s", res.Stdout)
	}
}

// A run that changes nothing writes nothing: the tree is byte-identical, no diff
// is printed, and the digest is unchanged. This is what makes init safe to run
// from a script.
func TestInitIsIdempotent(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	args := []string{"init", "--modules", "com.inductiveautomation.perspective"}
	testrig.WantExit(t, env.RunIn(dir, args...), contract.ExitOK)

	base := env.Snapshot()
	res := env.RunIn(dir, args...)
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaks(t)
	if res.Stderr != "" {
		t.Errorf("a no-op run printed a diff:\n%s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "(unchanged") {
		t.Errorf("a no-op run did not report the files as unchanged:\n%s", res.Stdout)
	}
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a second identical run changed %v", changes)
	}

	var data struct {
		Contract struct {
			Action string `json:"action"`
			Diff   string `json:"diff"`
			Digest string `json:"digest"`
		} `json:"contract"`
	}
	res = env.RunIn(dir, "init", "--json", "--modules", "com.inductiveautomation.perspective")
	testrig.WantExit(t, res, contract.ExitOK)
	testrig.DataOf(t, res.Stdout, &data)
	if data.Contract.Action != "unchanged" || data.Contract.Diff != "" {
		t.Errorf("second run: action %q diff %q, want unchanged and no diff", data.Contract.Action, data.Contract.Diff)
	}
	if data.Contract.Digest == "" {
		t.Error("unchanged run reported no digest")
	}
}

// The .gitignore entry is managed: added once, kept once, and never duplicated
// by a later run, whatever spelling the repository already used.
func TestInitGitignoreEntryIsManagedOnce(t *testing.T) {
	t.Run("appends to an existing gitignore", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")
		env.Write("repo/.gitignore", "bin/\n*.tmp\n")

		for range 2 {
			testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
		}
		raw, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		body := string(raw)
		if !strings.HasPrefix(body, "bin/\n*.tmp\n") {
			t.Errorf("existing entries were not preserved:\n%s", body)
		}
		if n := strings.Count(body, ".igdev/"); n != 1 {
			t.Errorf(".igdev/ appears %d times, want once:\n%s", n, body)
		}
	})

	t.Run("leaves an equivalent entry alone", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")
		env.Write("repo/.gitignore", "bin/\n/.igdev/\n")
		base := env.Snapshot()

		res := env.RunIn(dir, "init")
		testrig.WantExit(t, res, contract.ExitOK)
		for _, change := range env.Changes(base) {
			if strings.HasSuffix(change.Path, ".gitignore") {
				t.Errorf(".gitignore was rewritten although it already ignores .igdev: %s", change.Kind)
			}
		}
	})
}

// init maintains a minimal, version-free managed block in AGENTS.md: created
// when the file has none, replaced in place when it does, preserved across
// re-runs, and never duplicated. The block is the only part igdev owns; text
// outside the markers survives.
func TestInitManagesAgentsBlock(t *testing.T) {
	const (
		startMark = "<!-- igdev:start -->"
		endMark   = "<!-- igdev:end -->"
	)

	t.Run("creates the block when AGENTS.md is absent", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")

		res := env.RunIn(dir, "init", "--json")
		testrig.WantExit(t, res, contract.ExitOK)

		raw, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("read AGENTS.md: %v", err)
		}
		body := string(raw)
		if !strings.Contains(body, startMark) || !strings.Contains(body, endMark) {
			t.Errorf("the managed block is missing its markers:\n%s", body)
		}
		if strings.Count(body, startMark) != 1 {
			t.Errorf("the block appears more than once:\n%s", body)
		}
		// Five lines, the markers inclusive, and version-free.
		if n := strings.Count(strings.TrimRight(body, "\n"), "\n") + 1; n != 5 {
			t.Errorf("the managed block is %d lines, want 5:\n%s", n, body)
		}
		if strings.Contains(body, "0.1.0") || strings.Contains(body, "contract") {
			t.Errorf("the managed block carries a version:\n%s", body)
		}

		var data struct {
			Agents struct {
				Action string `json:"action"`
			} `json:"agents"`
		}
		testrig.DataOf(t, res.Stdout, &data)
		if data.Agents.Action != "created" {
			t.Errorf("agents action = %q, want created", data.Agents.Action)
		}
	})

	t.Run("appends to existing AGENTS.md content", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")
		env.Write("repo/AGENTS.md", "# House rules\n\nBe kind.\n")

		for range 2 {
			testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("read AGENTS.md: %v", err)
		}
		body := string(raw)
		if !strings.HasPrefix(body, "# House rules\n\nBe kind.\n") {
			t.Errorf("existing content was not preserved:\n%s", body)
		}
		if strings.Count(body, startMark) != 1 {
			t.Errorf("running init twice duplicated the block:\n%s", body)
		}
	})

	t.Run("replaces the block in place", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")
		env.Write("repo/AGENTS.md", startMark+"\nstale block body\n"+endMark+"\n# after\n")

		res := env.RunIn(dir, "init", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		var data struct {
			Agents struct {
				Action string `json:"action"`
				Diff   string `json:"diff"`
			} `json:"agents"`
		}
		testrig.DataOf(t, res.Stdout, &data)
		if data.Agents.Action != "updated" {
			t.Errorf("agents action = %q, want updated", data.Agents.Action)
		}
		if !strings.Contains(data.Agents.Diff, "-stale block body") {
			t.Errorf("the printed diff does not show the replaced block:\n%s", data.Agents.Diff)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("read AGENTS.md: %v", err)
		}
		body := string(raw)
		if strings.Contains(body, "stale block body") {
			t.Errorf("the stale block body survived:\n%s", body)
		}
		if !strings.HasSuffix(body, "# after\n") {
			t.Errorf("content after the block was not preserved:\n%s", body)
		}
		if strings.Count(body, startMark) != 1 {
			t.Errorf("the block was duplicated:\n%s", body)
		}
	})

	t.Run("re-run leaves an up-to-date block alone", func(t *testing.T) {
		env := testrig.NewEnv(t)
		dir := env.Mkdir("repo")
		testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
		base := env.Snapshot()

		res := env.RunIn(dir, "init", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		if changes := env.Changes(base); len(changes) != 0 {
			t.Errorf("a second init changed the tree: %v", changes)
		}
	})
}

// A value the contract schema cannot hold is the invocation's fault, and nothing
// is written: a bad flag must not leave a half-formed contract behind.
func TestInitRejectsBadFlagValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		value string
	}{
		{"version", []string{"--ignition-version", "latest"}, "latest"},
		{"jython", []string{"--jython-version", "two"}, "two"},
		{"edition", []string{"--edition", "Standard Edition"}, "Standard Edition"},
		{"heap", []string{"--gateway-memory-mb", "-1"}, "-1"},
		{"timezone", []string{"--gateway-timezone", "Mars Olympus"}, "Mars Olympus"},
		{"min version", []string{"--tool-min-version", "soon"}, "soon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testrig.NewEnv(t)
			dir := env.Mkdir("repo")
			base := env.Snapshot()

			res := env.RunIn(dir, append([]string{"init", "--json"}, tc.args...)...)
			testrig.WantExit(t, res, contract.ExitUsage)
			envelope := testrig.Envelope(t, res.Stdout)
			testrig.WantCode(t, envelope, contract.CodeUsage)
			if !strings.Contains(envelope.Message, tc.value) {
				t.Errorf("message %q does not name the rejected value", envelope.Message)
			}
			if changes := env.Changes(base); len(changes) != 0 {
				t.Errorf("a refused run wrote %v", changes)
			}
		})
	}
}

// The write is atomic and leaves no debris: no temp file survives in the target
// directory or in TMPDIR, no backup copy appears next to the contract, and a file
// that already had its own permissions keeps them across a rewrite.
func TestInitWritesAtomicallyAndKeepsMode(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	contractPath := filepath.Join(dir, "igdev.toml")
	if err := os.Chmod(contractPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	res := env.RunIn(dir, "init", "--gateway-memory-mb", "4096")
	testrig.WantExit(t, res, contract.ExitOK)
	env.AssertNoPartialWrites(t)
	env.AssertTempDirEmpty(t)

	info, err := os.Stat(contractPath)
	if err != nil {
		t.Fatalf("stat contract: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("contract mode = %o after a rewrite, want the 600 it had", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		switch {
		case entry.Name() == "igdev.toml", entry.Name() == ".gitignore", entry.Name() == "AGENTS.md":
		default:
			t.Errorf("init left %s behind in the repository", entry.Name())
		}
	}
}

// `igdev help init` is the complete local reference for the only tracked-file
// writer: every flag is listed with its default, so an agent never has to guess.
func TestHelpForInit(t *testing.T) {
	env := testrig.NewEnv(t)

	res := env.MustRun("help", "init")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "help_init.txt", res.Stdout)
	for _, flag := range []string{
		"--ignition-version", "--jython-version", "--edition", "--modules",
		"--scan-jython", "--scan-capabilities", "--command-check", "--command-test",
		"--command-build", "--command-smoke", "--gateway-memory-mb", "--gateway-timezone",
		"--tool-min-version", "--json",
	} {
		if !strings.Contains(res.Stdout, flag) {
			t.Errorf("init help does not document %s", flag)
		}
	}

	machine := env.MustRun("help", "init", "--json")
	testrig.WantExit(t, machine, contract.ExitOK)
	envelope := testrig.Envelope(t, machine.Stdout)
	var data struct {
		Command string `json:"command"`
		Help    string `json:"help"`
		Section string `json:"section"`
	}
	testrig.DataOf(t, machine.Stdout, &data)
	if data.Command != "igdev init" || data.Section != "init" {
		t.Errorf("machine help named %q/%q", data.Command, data.Section)
	}
	if data.Help != res.Stdout {
		t.Error("machine help differs from the human reference")
	}
	if envelope.Ok != true {
		t.Errorf("help envelope not ok: %+v", envelope)
	}
}

// init edits the Project Contract discovery finds, not a new file in the current
// directory: running it from a subdirectory is the same as running it at the root.
func TestInitEditsDiscoveredProjectRoot(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	nested := env.Mkdir("repo/ignition/script-python")

	res := env.RunIn(nested, "init", "--name", "fixture", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	var data struct {
		Contract struct {
			Path   string `json:"path"`
			Action string `json:"action"`
		} `json:"contract"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if got := env.Normalize(data.Contract.Path); got != "<ROOT>/repo/igdev.toml" {
		t.Errorf("contract path = %s, want the discovered Project Root's contract", got)
	}
	if data.Contract.Action != "updated" {
		t.Errorf("action = %q, want updated", data.Contract.Action)
	}
	if _, err := os.Stat(filepath.Join(nested, "igdev.toml")); !os.IsNotExist(err) {
		t.Error("init wrote a second contract in the working directory")
	}
}

// init is the repair path: a contract igdev cannot parse, or one declaring a
// schema this binary does not speak, is rewritten rather than partially read —
// and the printed diff is the review of what that costs.
func TestInitRepairsUnreadableContract(t *testing.T) {
	cases := []struct {
		name     string
		contract string
	}{
		{"malformed TOML", "schema = 1\n\n[ignition\nversion = broken\n"},
		{"unknown key", "schema = 1\n\n[ignition]\nversion = \"8.3.8\"\njython_verison = \"2.7.4\"\n"},
		{"future schema", "schema = 2\n\n[ignition]\nversion = \"9.0.0\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testrig.NewEnv(t)
			dir := env.Project("repo", tc.contract)

			res := env.RunIn(dir, "init", "--json")
			testrig.WantExit(t, res, contract.ExitOK)

			var data struct {
				Contract struct {
					Action string `json:"action"`
					Diff   string `json:"diff"`
				} `json:"contract"`
			}
			testrig.DataOf(t, res.Stdout, &data)
			if data.Contract.Action != "updated" {
				t.Errorf("action = %q, want updated", data.Contract.Action)
			}
			if !strings.Contains(data.Contract.Diff, "schema = 1") {
				t.Errorf("diff does not show the rewrite:\n%s", data.Contract.Diff)
			}

			// The repaired contract must be one the CLI itself accepts.
			status := env.RunIn(dir, "status", "--json")
			testrig.WantExit(t, status, contract.ExitOK)
			parsed := testrig.Status(t, status.Stdout)
			if !parsed.Contract.SchemaSupported {
				t.Errorf("the repaired contract is still unsupported:\n%s", status.Stdout)
			}
		})
	}
}
