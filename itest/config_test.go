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
	const key = "project.ignition_version"

	root := env.Project("repo", `schema = 1

[project]
name = "precedence"
ignition_version = "8.1.21"
`)
	env.LocalConfig("repo", "project.ignition_version = \"8.2.0\"\n")
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
	root := env.Project("repo", "schema = 1\n\n[project]\nignition_version = \"8.1.21\"\n")
	env.LocalConfig("repo", "[project]\nignition_version = \"8.2.0\"\n")

	res := env.RunIn(root, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	wantSource(t, res, "project.ignition_version", "8.2.0", "local")

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
