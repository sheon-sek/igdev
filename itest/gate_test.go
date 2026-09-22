package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/buildinfo"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The Gate refuses to let a project command do work on a checkout that is not
// ready, and its refusals are the frozen agent contract.
//
// Ticket 08 wires the Gate's enforcement here rather than onto a verb, because
// the first gated verb (`setup` and then the check pipeline) arrives in the next
// tickets. The fixtures below are produced by the real CLI — `igdev init` writes
// the contract, the rig writes the stamp `igdev setup` will own — so what is
// frozen is the Gate's verdict on real files, not a hand-built struct.

// gateInputOf reads a checkout the way the CLI does and hands it to the Gate.
func gateInputOf(t *testing.T, env *testrig.Env, dir string) gate.Input {
	t.Helper()
	found, err := project.Discover(dir)
	if err != nil {
		t.Fatalf("discover %s: %v", dir, err)
	}
	return gate.Input{
		InProject:      found.InProject(),
		StartDir:       found.StartDir,
		ContractPath:   found.Contract.Path,
		ContractRaw:    found.ContractTOML,
		SetupPresent:   found.Setup,
		SetupPath:      found.SetupPath,
		SetupRaw:       found.SetupRaw,
		SetupReadError: found.SetupReadError,
		CLIVersion:     buildinfo.Version,
		CLIContract:    contract.Version,
	}
}

// gateEnvelope renders the Gate's refusal the way the CLI would report it, after
// checking the envelope against the frozen key order.
func gateEnvelope(t *testing.T, env *testrig.Env, in gate.Input) string {
	t.Helper()
	_, fault := gate.Evaluate(in)
	if fault == nil {
		t.Fatalf("the Gate accepted %+v", in)
	}
	raw, err := contract.Encode(contract.Failure(fault))
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	testrig.Envelope(t, string(raw))
	return env.Normalize(string(raw))
}

// A contract with no Checkout Setup at all: nothing has materialized this
// checkout yet.
func TestGateRefusesMissingCheckoutSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	got := gateEnvelope(t, env, gateInputOf(t, env, dir))
	env.Golden(t, "gate_setup_required.json", got)

	envelope := testrig.Envelope(t, got)
	testrig.WantCode(t, envelope, contract.CodeSetupRequired)
	testrig.WantRemediation(t, envelope, "igdev setup")
	if !strings.Contains(envelope.Message, ".igdev/setup.json") {
		t.Errorf("message does not name the missing record: %q", envelope.Message)
	}
}

// Hand-editing the Project Contract after setup moves the Contract Digest, so
// the Setup Stamp no longer matches: the Gate reports the checkout as stale until
// `igdev setup` runs again.
func TestGateRefusesStaleCheckoutSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	env.SetupStamp("repo", env.Path("repo", "igdev.toml"))

	current := gateInputOf(t, env, dir)
	state, fault := gate.Evaluate(current)
	if fault != nil {
		t.Fatalf("a freshly stamped checkout was refused: %v", fault)
	}
	if state.Stamp != gate.StampCurrent {
		t.Fatalf("stamp = %s, want %s", state.Stamp, gate.StampCurrent)
	}

	// A hand-edit: the file the reviewer would touch, changed outside the CLI.
	env.Write("repo/igdev.toml", string(current.ContractRaw)+"\n[commands]\ncheck = \"./check.sh\"\n")

	got := gateEnvelope(t, env, gateInputOf(t, env, dir))
	env.Golden(t, "gate_setup_stale.json", got)

	envelope := testrig.Envelope(t, got)
	testrig.WantCode(t, envelope, contract.CodeSetupStale)
	testrig.WantRemediation(t, envelope, "igdev setup")
	if !strings.Contains(envelope.Message, "Contract Digest mismatch") {
		t.Errorf("message does not say what moved: %q", envelope.Message)
	}
}

// A contract from a future schema version fails closed: never partially read,
// never auto-downgraded, with both ways out named.
func TestGateRefusesUnsupportedSchema(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", "schema = 2\n\n[ignition]\nversion = \"9.0.0\"\n\n[future]\nnothing = true\n")

	got := gateEnvelope(t, env, gateInputOf(t, env, dir))
	env.Golden(t, "gate_schema_unsupported.json", got)

	envelope := testrig.Envelope(t, got)
	testrig.WantCode(t, envelope, contract.CodeContractSchemaUnsupported)
	testrig.WantRemediation(t, envelope, "igdev init")
	if len(envelope.Remediation) < 2 {
		t.Errorf("remediation names only one way out: %+v", envelope.Remediation)
	}
}

// A project that requires a newer igdev than the one running is refused, and the
// way out is installing one.
func TestGateRefusesOlderCLI(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init", "--tool-min-version", "99.0.0"), contract.ExitOK)
	env.SetupStamp("repo", env.Path("repo", "igdev.toml"))

	got := gateEnvelope(t, env, gateInputOf(t, env, dir))
	env.Golden(t, "gate_version_unsupported.json", got)

	envelope := testrig.Envelope(t, got)
	testrig.WantCode(t, envelope, contract.CodeVersionUnsupported)
	if !strings.Contains(envelope.Remediation[0].Command, "install.sh") {
		t.Errorf("remediation does not point at installing a newer igdev: %+v", envelope.Remediation)
	}
}

// Nothing to act on: a project command outside any Project Root.
func TestGateRefusesOutsideProject(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("plain")

	got := gateEnvelope(t, env, gateInputOf(t, env, dir))
	env.Golden(t, "gate_not_initialized.json", got)
	testrig.WantCode(t, testrig.Envelope(t, got), contract.CodeNotInitialized)
}

// A current checkout passes: no fault, and the state says why.
func TestGateAcceptsCurrentCheckout(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	env.SetupStamp("repo", env.Path("repo", "igdev.toml"))

	in := gateInputOf(t, env, dir)
	if err := gate.Require(in); err != nil {
		t.Fatalf("Require() refused a current checkout: %v", err)
	}
	state, fault := gate.Evaluate(in)
	if fault != nil {
		t.Fatalf("Evaluate() = %v, want no fault", fault)
	}
	if state.Stamp != gate.StampCurrent || !state.SchemaSupported || state.Digest == "" {
		t.Errorf("state = %+v, want a current, supported, digested contract", state)
	}
}

// Rewriting the contract with the CLI moves the digest with it, so a checkout
// set up against the old bytes is stale until setup runs again — the same rule
// that makes a hand-edit visible.
func TestGateSeesCLIWrittenContractChange(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	env.SetupStamp("repo", env.Path("repo", "igdev.toml"))
	if err := gate.Require(gateInputOf(t, env, dir)); err != nil {
		t.Fatalf("a fresh checkout was refused: %v", err)
	}

	testrig.WantExit(t, env.RunIn(dir, "init", "--gateway-memory-mb", "4096"), contract.ExitOK)

	_, fault := gate.Evaluate(gateInputOf(t, env, dir))
	if fault == nil {
		t.Fatal("a contract rewritten by init left the Setup current")
	}
	if code := contract.AsFault(fault).Code; code != contract.CodeSetupStale {
		t.Errorf("code = %s, want %s", code, contract.CodeSetupStale)
	}
}
