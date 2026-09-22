package gate

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
)

// contractFor returns a valid schema v1 contract with the given extra keys.
func contractFor(extra string) []byte {
	return []byte("schema = 1\n\n[ignition]\nversion = \"8.3.8\"\njython_version = \"2.7.4\"\nedition = \"standard\"\n" + extra)
}

// inputFor describes a checkout: its contract bytes and, optionally, a Setup
// record whose stamp is derived from those bytes.
func inputFor(t *testing.T, raw []byte, stamp func(Stamp) Stamp) Input {
	t.Helper()
	in := Input{
		InProject:    true,
		StartDir:     "/repo",
		ContractPath: "/repo/igdev.toml",
		ContractRaw:  raw,
		SetupPath:    "/repo/.igdev/setup.json",
		CLIVersion:   "0.1.0",
		CLIContract:  contract.Version,
	}
	if stamp != nil {
		record := Stamp{
			Schema:         StampSchema,
			ContractDigest: project.Digest(raw),
			ContractSchema: project.DeclaredSchema(raw),
			CLIContract:    contract.Version,
		}
		record = stamp(record)
		encoded, err := record.Encode()
		if err != nil {
			t.Fatalf("encode stamp: %v", err)
		}
		in.SetupPresent = true
		in.SetupRaw = encoded
	}
	return in
}

func TestEvaluateCannotStart(t *testing.T) {
	cases := []struct {
		name    string
		build   func(t *testing.T) Input
		code    contract.Code
		message string
	}{
		{
			name:    "no project contract",
			build:   func(*testing.T) Input { return Input{StartDir: "/nowhere"} },
			code:    contract.CodeNotInitialized,
			message: "no Project Contract",
		},
		{
			name: "unsupported contract schema",
			build: func(t *testing.T) Input {
				return inputFor(t, []byte("schema = 2\n\n[ignition]\nversion = \"9.0.0\"\n"), nil)
			},
			code:    contract.CodeContractSchemaUnsupported,
			message: "schema 2",
		},
		{
			name: "missing checkout setup",
			build: func(t *testing.T) Input {
				return inputFor(t, contractFor(""), nil)
			},
			code:    contract.CodeSetupRequired,
			message: "no Checkout Setup",
		},
		{
			name: "contract edited after setup",
			build: func(t *testing.T) Input {
				in := inputFor(t, contractFor(""), func(s Stamp) Stamp { return s })
				in.ContractRaw = []byte(string(in.ContractRaw) + "\n[commands]\ncheck = \"./check.sh\"\n")
				return in
			},
			code:    contract.CodeSetupStale,
			message: "Contract Digest mismatch",
		},
		{
			name: "setup record from another contract schema",
			build: func(t *testing.T) Input {
				return inputFor(t, contractFor(""), func(s Stamp) Stamp { s.ContractSchema = 0; return s })
			},
			code:    contract.CodeSetupStale,
			message: "Project Contract schema",
		},
		{
			name: "setup record from another CLI contract version",
			build: func(t *testing.T) Input {
				return inputFor(t, contractFor(""), func(s Stamp) Stamp { s.CLIContract = "0"; return s })
			},
			code:    contract.CodeSetupStale,
			message: "CLI Contract Version",
		},
		{
			name: "setup record in an old format",
			build: func(t *testing.T) Input {
				return inputFor(t, contractFor(""), func(s Stamp) Stamp { s.Schema = 0; return s })
			},
			code:    contract.CodeSetupStale,
			message: "record is schema",
		},
		{
			name: "unreadable setup record",
			build: func(t *testing.T) Input {
				in := inputFor(t, contractFor(""), nil)
				in.SetupPresent = true
				in.SetupReadError = "permission denied"
				return in
			},
			code:    contract.CodeSetupStale,
			message: "cannot be read",
		},
		{
			name: "setup record that is not JSON",
			build: func(t *testing.T) Input {
				in := inputFor(t, contractFor(""), nil)
				in.SetupPresent = true
				in.SetupRaw = []byte("{not json")
				return in
			},
			code:    contract.CodeSetupStale,
			message: "not valid JSON",
		},
		{
			name: "contract that does not parse",
			build: func(t *testing.T) Input {
				return inputFor(t, []byte("schema = 1\n\n[ignition\nversion = broken\n"), nil)
			},
			code:    contract.CodeConfigInvalid,
			message: "not valid TOML",
		},
		{
			name: "contract with an unknown key",
			build: func(t *testing.T) Input {
				return inputFor(t, []byte("schema = 1\n\n[ignition]\nversion = \"8.3.8\"\njython_verison = \"2.7.4\"\n"), nil)
			},
			code:    contract.CodeConfigInvalid,
			message: "jython_verison",
		},
		{
			name: "project requires a newer igdev",
			build: func(t *testing.T) Input {
				raw := contractFor("\n[tool]\nmin_version = \"99.0.0\"\n")
				return inputFor(t, raw, func(s Stamp) Stamp { return s })
			},
			code:    contract.CodeVersionUnsupported,
			message: "requires igdev >= 99.0.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fault := Evaluate(tc.build(t))
			if fault == nil {
				t.Fatalf("%s: the Gate returned no fault", tc.name)
			}
			if fault.Code != tc.code {
				t.Errorf("code = %s, want %s (message %q)", fault.Code, tc.code, fault.Message)
			}
			if fault.Exit != contract.ExitFailure {
				t.Errorf("exit = %d, want %d (a Gate refusal is an ordinary command failure)",
					fault.Exit, contract.ExitFailure)
			}
			if !strings.Contains(fault.Message, tc.message) {
				t.Errorf("message %q does not contain %q", fault.Message, tc.message)
			}
			if len(fault.Remediation) == 0 || fault.Remediation[0].Command == "" {
				t.Errorf("fault carries no remediation: %+v", fault.Remediation)
			}
		})
	}
}

// The remedy for a missing or stale Setup is the same exact command, so an agent
// never has to invent a repair.
func TestSetupFaultsRemediateSetup(t *testing.T) {
	for _, in := range []Input{
		inputFor(t, contractFor(""), nil),
		func() Input {
			in := inputFor(t, contractFor(""), func(s Stamp) Stamp { return s })
			in.ContractRaw = []byte(string(in.ContractRaw) + "\n[commands]\ncheck = \"x\"\n")
			return in
		}(),
	} {
		_, fault := Evaluate(in)
		if fault == nil || fault.Remediation[0].Command != "igdev setup" {
			t.Fatalf("fault %+v does not remediate with igdev setup", fault)
		}
	}
}

// A current Checkout Setup passes, and the state reports it.
func TestEvaluateCurrentSetup(t *testing.T) {
	in := inputFor(t, contractFor("\n[tool]\nmin_version = \"0.0.1\"\n"), func(s Stamp) Stamp { return s })
	state, fault := Evaluate(in)
	if fault != nil {
		t.Fatalf("a current checkout was refused: %v", fault)
	}
	if state.Stamp != StampCurrent {
		t.Errorf("stamp = %s, want %s", state.Stamp, StampCurrent)
	}
	if !state.SchemaSupported {
		t.Error("schema_supported = false for schema 1")
	}
	if state.Digest != project.Digest(in.ContractRaw) {
		t.Errorf("digest = %s, want the contract digest", state.Digest)
	}
	if err := Require(in); err != nil {
		t.Errorf("Require() = %v, want nil", err)
	}
}

// A version the CLI cannot compare — a development build — is not a refusal.
func TestMinVersionIgnoresUnparseableCLI(t *testing.T) {
	in := inputFor(t, contractFor("\n[tool]\nmin_version = \"99.0.0\"\n"), func(s Stamp) Stamp { return s })
	in.CLIVersion = "dev"
	if _, fault := Evaluate(in); fault != nil {
		t.Errorf("a dev build was refused on an uncomparable version: %v", fault)
	}
}

// Require is the enforcement entry point: it returns the same fault Evaluate
// reports, and nil only when the checkout may proceed.
func TestRequireReturnsTheFault(t *testing.T) {
	in := inputFor(t, contractFor(""), nil)
	err := Require(in)
	if err == nil {
		t.Fatal("Require() accepted a checkout with no Setup")
	}
	if code := contract.AsFault(err).Code; code != contract.CodeSetupRequired {
		t.Errorf("code = %s, want %s", code, contract.CodeSetupRequired)
	}
}
