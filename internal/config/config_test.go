package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Precedence is a pure function, so the tiers that the golden suite can only
// show one at a time are pinned exhaustively here.
func TestResolvePrecedence(t *testing.T) {
	const key = "ignition.version"
	cases := []struct {
		name       string
		in         Input
		wantValue  string
		wantSource Source
	}{
		{
			name:       "nothing set uses the embedded default",
			in:         Input{},
			wantValue:  IgnitionTarget,
			wantSource: SourceDefault,
		},
		{
			name:       "contract is the lowest file tier",
			in:         Input{ContractTOML: []byte("[ignition]\nversion = \"8.1.21\"\n"), ContractPath: "/p/igdev.toml"},
			wantValue:  "8.1.21",
			wantSource: SourceContract,
		},
		{
			name: "local config outranks the contract",
			in: Input{
				ContractTOML: []byte("[ignition]\nversion = \"8.1.21\"\n"), ContractPath: "/p/igdev.toml",
				LocalTOML: []byte("ignition.version = \"8.2.0\"\n"), LocalPath: "/p/.igdev/local.toml",
			},
			wantValue:  "8.2.0",
			wantSource: SourceLocal,
		},
		{
			name: "environment outranks local config",
			in: Input{
				ContractTOML: []byte("[ignition]\nversion = \"8.1.21\"\n"), ContractPath: "/p/igdev.toml",
				LocalTOML: []byte("ignition.version = \"8.2.0\"\n"), LocalPath: "/p/.igdev/local.toml",
				Environ: []string{"IGDEV_IGNITION_VERSION=8.3.2"},
			},
			wantValue:  "8.3.2",
			wantSource: SourceEnv,
		},
		{
			name: "flag outranks the environment",
			in: Input{
				Flags:   map[string]string{key: "9.9.9"},
				Environ: []string{"IGDEV_IGNITION_VERSION=8.3.2"},
			},
			wantValue:  "9.9.9",
			wantSource: SourceFlag,
		},
		{
			name:       "flat dotted keys work as well as tables",
			in:         Input{ContractTOML: []byte("ignition.version = \"8.1.26\"\n"), ContractPath: "/p/igdev.toml"},
			wantValue:  "8.1.26",
			wantSource: SourceContract,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Resolve(tc.in)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got := res.Get(key)
			if got.Value != tc.wantValue {
				t.Errorf("%s = %v, want %s", key, got.Value, tc.wantValue)
			}
			if got.Source != tc.wantSource {
				t.Errorf("%s came from %s, want %s", key, got.Source, tc.wantSource)
			}
		})
	}
}

// Every frozen key is reported with a winning tier, which is what makes
// precedence observable in status output.
func TestResolveReportsEverySchemaKey(t *testing.T) {
	res, err := Resolve(Input{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	values := res.Values()
	if len(values) != len(Schema) {
		t.Fatalf("reported %d keys, schema has %d", len(values), len(Schema))
	}
	for i, v := range values {
		if i > 0 && values[i-1].Key > v.Key {
			t.Errorf("keys are not sorted: %s before %s", values[i-1].Key, v.Key)
		}
		if v.Source != SourceDefault {
			t.Errorf("%s source = %s, want default with no tiers set", v.Key, v.Source)
		}
	}
}

// A value of the wrong type is a named failure, and the flag tier is blamed on
// the invocation while every other tier is machine state.
func TestResolveTypeValidation(t *testing.T) {
	cases := []struct {
		name     string
		in       Input
		wantExit contract.Exit
		wantCode contract.Code
	}{
		{
			name:     "bad flag value",
			in:       Input{Flags: map[string]string{"updater.enabled": "maybe"}},
			wantExit: contract.ExitUsage,
			wantCode: contract.CodeUsage,
		},
		{
			name:     "bad environment value",
			in:       Input{Environ: []string{"IGDEV_UPDATER_TIMEOUT_SECONDS=soon"}},
			wantExit: contract.ExitFailure,
			wantCode: contract.CodeConfigInvalid,
		},
		{
			name:     "wrong type in a file tier",
			in:       Input{LocalTOML: []byte("updater.enabled = 3\n"), LocalPath: "/p/.igdev/local.toml"},
			wantExit: contract.ExitFailure,
			wantCode: contract.CodeConfigInvalid,
		},
		{
			name:     "output format outside its enum",
			in:       Input{Environ: []string{"IGDEV_OUTPUT_FORMAT=yaml"}},
			wantExit: contract.ExitFailure,
			wantCode: contract.CodeConfigInvalid,
		},
		{
			name:     "malformed toml",
			in:       Input{ContractTOML: []byte("[project\nname = x\n"), ContractPath: "/p/igdev.toml"},
			wantExit: contract.ExitFailure,
			wantCode: contract.CodeConfigInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.in)
			var f *contract.Fault
			if !errors.As(err, &f) {
				t.Fatalf("Resolve() error = %v, want a *contract.Fault", err)
			}
			if f.Code != tc.wantCode || f.Exit != tc.wantExit {
				t.Errorf("fault = %s/exit %d, want %s/exit %d (%s)", f.Code, f.Exit, tc.wantCode, tc.wantExit, f.Message)
			}
			if len(f.Remediation) == 0 || f.Remediation[0].Command == "" {
				t.Errorf("fault carries no machine-readable remediation: %+v", f)
			}
		})
	}
}

// Environment booleans spell several ways; an empty value means "not set" rather
// than false, which is how a test clears the notice kill switch.
func TestParseEnvValues(t *testing.T) {
	res, err := Resolve(Input{Environ: []string{
		"IGDEV_UPDATER_ENABLED=no",
		"IGDEV_OUTPUT_FORMAT=",
	}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Bool("updater.enabled") {
		t.Errorf("updater.enabled = true, want false for no")
	}
	if got := res.Get("output.format"); got.Source != SourceDefault {
		t.Errorf("an empty IGDEV_OUTPUT_FORMAT resolved from %s, want default", got.Source)
	}
}

// An explicit empty override is a value, not an absence: the flag tier wins with
// the empty string, which is how a caller clears a contract-declared version.
func TestFlagTierEmptyValueIsDeliberate(t *testing.T) {
	res, err := Resolve(Input{
		Flags:        map[string]string{"ignition.version": ""},
		ContractTOML: []byte("[ignition]\nversion = \"8.1.21\"\n"), ContractPath: "/p/igdev.toml",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := res.Get("ignition.version")
	if got.Source != SourceFlag || got.Value != "" {
		t.Errorf("resolved %q from %s, want the empty value from the flag tier", got.Value, got.Source)
	}
}

// Unknown keys inside a tier are ignored, not rejected: the Project Contract
// carries sections owned by later tickets.
func TestResolveIgnoresUnknownFileKeys(t *testing.T) {
	res, err := Resolve(Input{ContractTOML: []byte("schema = 1\n\n[commands]\ncheck = \"make check\"\n"), ContractPath: "/p/igdev.toml"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.String("ignition.version") != IgnitionTarget {
		t.Errorf("unknown contract sections changed a resolved default")
	}
}

// The kill switch is environment-only and must not be a config key: it overrides
// every tier, including the flag tier.
func TestNotifierDisabled(t *testing.T) {
	env := EnvironMap([]string{NotifierDisabledEnv + "=1"})
	if !NotifierDisabled(env) {
		t.Errorf("%s=1 did not disable the notifier", NotifierDisabledEnv)
	}
	for _, value := range []string{"", "0", "false", "no"} {
		if NotifierDisabled(EnvironMap([]string{NotifierDisabledEnv + "=" + value})) {
			t.Errorf("%s=%q unexpectedly disabled the notifier", NotifierDisabledEnv, value)
		}
	}
}

// EnvFor and the schema must agree, because the test rig points at the environment
// name rather than the key.
func TestSchemaEnvNamesAreFrozen(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Schema {
		if !strings.HasPrefix(k.Env, "IGDEV_") {
			t.Errorf("%s uses %q, which is outside the IGDEV_ namespace", k.Path, k.Env)
		}
		if seen[k.Env] {
			t.Errorf("two keys share the environment name %s", k.Env)
		}
		seen[k.Env] = true
		if strings.Count(k.Path, ".") == 0 {
			t.Errorf("%s has no section prefix", k.Path)
		}
	}
}
