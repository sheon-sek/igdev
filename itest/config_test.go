package itest

import (
	"os"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The frozen precedence rule is flags > IGDEV_* env > .igdev/local.toml >
// igdev.toml > embedded defaults. status --json reports the winning tier per
// key, so the rule is observable rather than merely documented.
func TestConfigPrecedenceTiers(t *testing.T) {
	env := testrig.NewEnv(t)
	const key = "ignition.version"

	root := env.Project("repo", `schema = 1

[project]
name = "precedence"

[ignition]
version = "8.1.21"
`)
	env.LocalConfig("repo", "ignition.version = \"8.2.0\"\n")
	envTier := testrig.EnvFor(key) + "=8.3.2"
	flagTier := "--config=" + key + "=9.9.9"

	t.Run("flag wins", func(t *testing.T) {
		env.SetBaseEnv(envTier)
		res := env.RunIn(root, "status", "--json", flagTier)
		testrig.WantExit(t, res, contract.ExitOK)
		env.Golden(t, "config_precedence_all_tiers.json", res.Stdout)
		wantSource(t, res, key, "9.9.9", "flag")
	})
	t.Run("env wins below flag", func(t *testing.T) {
		res := env.RunIn(root, "status", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		wantSource(t, res, key, "8.3.2", "env")
	})
	t.Run("local file wins below env", func(t *testing.T) {
		env.SetBaseEnv(testrig.EnvFor(key) + "=")
		res := env.RunIn(root, "status", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		wantSource(t, res, key, "8.2.0", "local")
	})
	t.Run("contract wins below local file", func(t *testing.T) {
		if err := os.Remove(env.Path("repo/.igdev/local.toml")); err != nil {
			t.Fatalf("remove local tier: %v", err)
		}
		res := env.RunIn(root, "status", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		wantSource(t, res, key, "8.1.21", "contract")
	})
	t.Run("embedded default wins last", func(t *testing.T) {
		if err := os.Remove(env.Path("repo/igdev.toml")); err != nil {
			t.Fatalf("remove contract tier: %v", err)
		}
		res := env.RunIn(root, "status", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		wantSource(t, res, key, "8.3.8", "default")
	})
}

// A broken setting must never cost the caller the dialect it asked for: the
// output.format tier is peeled on its own, skipping whatever else is unreadable.
func TestDialectSurvivesConfigFailures(t *testing.T) {
	cases := []struct {
		name    string
		baseEnv []string
		files   func(env *testrig.Env, root string)
		args    []string
	}{
		{
			name:    "unrelated bad value in an env tier",
			baseEnv: []string{"IGDEV_OUTPUT_FORMAT=json", "IGDEV_UPDATER_TIMEOUT_SECONDS=soon"},
			args:    []string{"status"},
		},
		{
			name:    "malformed local tier below a valid env dialect",
			baseEnv: []string{"IGDEV_OUTPUT_FORMAT=json"},
			files:   func(env *testrig.Env, root string) { env.LocalConfig(root, "[project\nname = broken\n") },
			args:    []string{"status"},
		},
		{
			name: "broken local tier above a valid contract dialect",
			files: func(env *testrig.Env, root string) {
				env.Project("repo", "schema = 1\noutput.format = \"json\"\n")
				env.LocalConfig(root, "updater.enabled = 7\n")
			},
			args: []string{"status"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testrig.NewEnv(t)
			root := env.Project("repo", "schema = 1\n")
			if tc.files != nil {
				tc.files(env, root)
			}
			for _, pair := range tc.baseEnv {
				env.SetBaseEnv(pair)
			}

			res := env.Run(testrig.Run{Args: tc.args, Dir: root})
			if res.Exit == 0 {
				t.Fatalf("expected a config failure, got exit 0\n%s", res.Stdout)
			}
			envelope := testrig.Envelope(t, res.Stdout)
			if envelope.Ok {
				t.Errorf("a failing command returned an ok envelope: %+v", envelope)
			}
			if envelope.Code != string(contract.CodeConfigInvalid) {
				t.Errorf("code = %q, want %s", envelope.Code, contract.CodeConfigInvalid)
			}
			if res.Stderr != "" {
				t.Errorf("stderr = %q, want the answer only on stdout in machine mode", res.Stderr)
			}
		})
	}
}

// An explicit --json=false is a flag-tier value, so it outranks a lower tier that
// asked for JSON.
func TestExplicitFalseJSONOverridesLowerTier(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n")
	env.LocalConfig(root, "output.format = \"json\"\n")

	// Without the flag the local tier still selects the machine dialect.
	jsonRun := env.RunIn(root, "status")
	testrig.WantExit(t, jsonRun, contract.ExitOK)
	if !strings.HasPrefix(jsonRun.Stdout, "{") {
		t.Fatalf("the local tier did not select JSON:\\n%s", firstLines(jsonRun.Stdout, 6))
	}
	wantSource(t, jsonRun, "output.format", "json", "local")

	textRun := env.RunIn(root, "status", "--json=false")
	testrig.WantExit(t, textRun, contract.ExitOK)
	if !strings.HasPrefix(textRun.Stdout, "project:   initialized") {
		t.Errorf("--json=false did not return to the human dialect:\n%s", firstLines(textRun.Stdout, 6))
	}
	env.Golden(t, "json_false_overrides_local.txt", textRun.Stdout)
}

func wantSource(t *testing.T, res testrig.Result, key, value, source string) {
	t.Helper()
	data := testrig.Status(t, res.Stdout)
	gotSource, gotValue, ok := data.ResolvedSource(key)
	if !ok {
		t.Fatalf("status does not report config key %q:\n%s", key, res.Stdout)
	}
	if gotValue != value || gotSource != source {
		t.Errorf("%s = %v [%s], want %s [%s]", key, gotValue, gotSource, value, source)
	}
}

// A file tier can select the machine dialect itself: output.format = json in
// .igdev/local.toml makes plain `igdev status` speak JSON with no flag.
func TestConfigFileTierSelectsDialect(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n")
	env.LocalConfig("repo", "output.format = \"json\"\n")

	res := env.RunIn(root, "status")
	testrig.WantExit(t, res, contract.ExitOK)

	envelope := testrig.Envelope(t, res.Stdout)
	if !envelope.Ok {
		t.Errorf("envelope not ok: %+v", envelope)
	}
	wantSource(t, res, "output.format", "json", "local")
}

// The Project Contract may also carry the key; the local tier still outranks it.
func TestConfigContractAndLocalCoexist(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n\n[ignition]\nversion = \"8.1.21\"\n")
	env.LocalConfig("repo", "[ignition]\nversion = \"8.2.0\"\n")

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	wantSource(t, res, "ignition.version", "8.2.0", "local")

	data := testrig.Status(t, res.Stdout)
	if _, ok := data.Config.TierFiles["contract"]; !ok {
		t.Errorf("tier_files does not name the contract tier: %+v", data.Config.TierFiles)
	}
	if _, ok := data.Config.TierFiles["local"]; !ok {
		t.Errorf("tier_files does not name the local tier: %+v", data.Config.TierFiles)
	}
}

// A value of the wrong type in any tier is IGDEV_E_CONFIG_INVALID and names the
// tier that set it, instead of silently falling back.
func TestConfigInvalidValueNamesTier(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n")
	env.LocalConfig("repo", "updater.enabled = \"maybe\"\n")

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConfigInvalid)
	if !strings.Contains(envelope.Message, "local") || !strings.Contains(envelope.Message, "updater.enabled") {
		t.Errorf("message does not name the tier and key: %q", envelope.Message)
	}
}

// A checkout-local config that exists but cannot be read must stop the run:
// silently dropping the tier would hand the caller config nobody asked for.
func TestUnreadableLocalTierFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	env := testrig.NewEnv(t)
	root := env.Project("repo", "schema = 1\n")
	path := env.LocalConfig("repo", "output.format = \"json\"\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	// The tier that cannot be read cannot state a dialect either, so machine mode
	// has to be asked for on the command line.
	res := env.RunIn(root, "status", "--json")
	if res.Exit == 0 {
		t.Fatalf("an unreadable tier was ignored:\n%s", res.Stdout)
	}
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConfigInvalid)
	if !strings.Contains(envelope.Message, "local.toml") {
		t.Errorf("fault does not name the unreadable file: %q", envelope.Message)
	}
	if len(envelope.Remediation) == 0 || !strings.Contains(envelope.Remediation[0].Command, "local.toml") {
		t.Errorf("fault carries no usable remediation: %+v", envelope.Remediation)
	}

	human := env.RunIn(root, "status")
	testrig.WantExit(t, human, contract.ExitFailure)
	if !strings.Contains(human.Stderr, "local.toml") || human.Stdout != "" {
		t.Errorf("human mode did not report the unreadable tier on stderr:\nstdout %q stderr %q",
			human.Stdout, human.Stderr)
	}
	res.AssertNoLeaks(t)
}

// A contract that carries sections the resolver does not know is still readable:
// unknown keys belong to later tickets, not to this ticket's error paths.
func TestConfigIgnoresUnknownContractSections(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", `schema = 1

[project]
name = "future"

[commands]
check = "./gradlew check"
`)

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	data := testrig.Status(t, res.Stdout)
	if !data.Initialized {
		t.Errorf("initialized = false with a contract present")
	}
}
