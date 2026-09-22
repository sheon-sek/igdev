// Package gate implements the Gate: the mandatory first-stage checks every
// project command passes before it does work — Project Root discovery (reported
// through project.Found), contract schema validation, Setup Stamp validation,
// and command prerequisites.
//
// Evaluate is pure: the caller hands in the contract bytes and the Checkout
// Setup record it read, so the same decisions run in the binary, in a unit test,
// and against a fixture. The enforcement entry point is Require; ticket 09 wires
// the first consumer (`setup`), and ticket 08 exercises it here and reports its
// verdict through `igdev status`.
package gate

import (
	"encoding/json"
	"fmt"

	"github.com/sheon-sek/igdev/internal/buildinfo"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/semver"
)

// StampSchema is the format version of the Checkout Setup record igdev writes.
const StampSchema = 1

// StampState is the Setup Stamp verdict for one checkout.
type StampState string

const (
	// StampRequired means there is no Checkout Setup record at all.
	StampRequired StampState = "required"
	// StampCurrent means the record matches the contract and this CLI.
	StampCurrent StampState = "current"
	// StampStale means a record exists but no longer describes this contract.
	StampStale StampState = "stale"
)

// Stamp is the Setup Stamp: what a Checkout Setup records to prove currency —
// the Contract Digest, the contract schema version, and the CLI Contract
// Version. Ticket 09 extends the same record with the Instance identity and the
// allocated ports; the stamp fields are frozen here.
type Stamp struct {
	// Schema is this record's own format version.
	Schema int `json:"schema"`
	// ContractDigest is project.Digest of the Project Contract bytes the setup
	// was materialized from.
	ContractDigest string `json:"contract_digest"`
	// ContractSchema is the Project Contract's declared schema version.
	ContractSchema int `json:"contract_schema"`
	// CLIContract is the CLI Contract Version that wrote the record.
	CLIContract string `json:"cli_contract"`
}

// Encode renders the record as the bytes `.igdev/setup.json` holds.
func (s Stamp) Encode() ([]byte, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Input is everything the Gate reads. It carries raw bytes, not paths, so a
// caller that already read them (discovery does) pays for nothing twice.
type Input struct {
	// InProject reports whether discovery found a Project Contract.
	InProject bool
	// StartDir is where the upward search began; it appears in the fault when
	// there is no contract to find.
	StartDir string
	// ContractPath and ContractRaw are the Project Contract file.
	ContractPath string
	ContractRaw  []byte
	// SetupPresent reports whether the Checkout Setup record exists.
	SetupPresent bool
	// SetupPath is where the record belongs.
	SetupPath string
	// SetupRaw is the record's bytes; nil when absent or unreadable.
	SetupRaw []byte
	// SetupReadError names why an existing record could not be read.
	SetupReadError string
	// CLIVersion is the release version of the running binary.
	CLIVersion string
	// CLIContract is the CLI Contract Version the running binary speaks.
	CLIContract string
}

// State is the Gate's verdict, whether or not enforcement stops the command.
// `igdev status` reports it so a stale checkout is visible before a command
// refuses to run.
type State struct {
	// Digest is the Contract Digest of the contract in hand; empty when there
	// is no contract.
	Digest string `json:"digest"`
	// Schema is the declared contract schema version.
	Schema int `json:"schema"`
	// SchemaSupported reports whether this CLI speaks that schema version.
	SchemaSupported bool `json:"schema_supported"`
	// Stamp is the Setup Stamp verdict.
	Stamp StampState `json:"stamp_state"`
}

// Evaluate runs the Gate and returns both the state and, when the checkout may
// not proceed, the fault to report. Checks run in the frozen order: schema,
// contract contents, Setup Stamp, then command prerequisites.
func Evaluate(in Input) (State, *contract.Fault) {
	state := State{Stamp: StampRequired}
	if !in.InProject {
		return state, notInitializedFault(in)
	}
	state.Digest = project.Digest(in.ContractRaw)
	state.Schema = project.DeclaredSchema(in.ContractRaw)
	state.SchemaSupported = state.Schema == project.LatestSchema
	state.Stamp = stampState(in, state.Digest, state.Schema)

	if state.Schema <= 0 {
		// Nothing usable was declared. ParseDoc names the real problem when the
		// file is malformed; a file that parses but says nothing is reported as
		// undeclared rather than as an unsupported version.
		if _, err := project.ParseDoc(in.ContractRaw, in.ContractPath); err != nil {
			return state, contract.AsFault(err)
		}
		return state, undeclaredSchemaFault(in)
	}
	if !state.SchemaSupported {
		return state, schemaUnsupportedFault(in, state.Schema)
	}
	doc, err := project.ParseDoc(in.ContractRaw, in.ContractPath)
	if err != nil {
		return state, contract.AsFault(err)
	}
	if state.Stamp != StampCurrent {
		return state, stampFault(in, state.Digest, state.Schema)
	}
	if fault := minVersionFault(in, doc); fault != nil {
		return state, fault
	}
	return state, nil
}

// Require is the Gate as a project command consumes it: nil when the checkout
// may proceed, otherwise the fault the caller returns unchanged. A command that
// mutates or reads project state calls this before doing anything else; `init`
// and `setup` are the repair paths and deliberately do not.
func Require(in Input) error {
	if _, fault := Evaluate(in); fault != nil {
		return fault
	}
	return nil
}

// stampState compares a Checkout Setup record against the contract.
func stampState(in Input, digest string, schema int) StampState {
	if !in.SetupPresent {
		return StampRequired
	}
	if in.SetupReadError != "" || in.SetupRaw == nil {
		return StampStale
	}
	var stamp Stamp
	if err := json.Unmarshal(in.SetupRaw, &stamp); err != nil {
		return StampStale
	}
	if stamp.Schema != StampSchema || stamp.ContractDigest != digest ||
		stamp.ContractSchema != schema || stamp.CLIContract != in.CLIContract {
		return StampStale
	}
	return StampCurrent
}

// staleReason explains a stale Setup in one clause, so the message says which
// of the stamp's fields moved instead of only that something did.
func staleReason(in Input, digest string, schema int) string {
	switch {
	case in.SetupReadError != "":
		return "the Checkout Setup record cannot be read: " + in.SetupReadError
	case in.SetupRaw == nil:
		return "the Checkout Setup record is missing"
	}
	var stamp Stamp
	if err := json.Unmarshal(in.SetupRaw, &stamp); err != nil {
		return "the Checkout Setup record is not valid JSON"
	}
	switch {
	case stamp.Schema != StampSchema:
		return fmt.Sprintf("the Checkout Setup record is schema %d, this igdev writes schema %d", stamp.Schema, StampSchema)
	case stamp.ContractSchema != schema:
		return fmt.Sprintf("the Checkout Setup was made for Project Contract schema %d, the contract declares %d",
			stamp.ContractSchema, schema)
	case stamp.ContractDigest != digest:
		return "the Project Contract changed since the last setup (Contract Digest mismatch)"
	case stamp.CLIContract != in.CLIContract:
		return fmt.Sprintf("the Checkout Setup was made by CLI Contract Version %s, this binary speaks %s",
			stamp.CLIContract, in.CLIContract)
	default:
		return "the Checkout Setup does not match this contract"
	}
}

func notInitializedFault(in Input) *contract.Fault {
	return contract.NewFault(contract.CodeNotInitialized, contract.ExitFailure,
		fmt.Sprintf("no Project Contract (%s) at or above %s", project.ContractFile, in.StartDir)).
		WithRemediation(contract.Remediation{
			Command: "igdev init",
			Why:     "write the Project Contract for this repository",
		})
}

func undeclaredSchemaFault(in Input) *contract.Fault {
	return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
		fmt.Sprintf("project contract %s does not declare a schema version (want `schema = %d`)",
			in.ContractPath, project.LatestSchema)).
		WithRemediation(contract.Remediation{
			Command: "igdev init",
			Why:     "rewrite igdev.toml at the supported schema layout (the diff shows what changes)",
		})
}

func schemaUnsupportedFault(in Input, declared int) *contract.Fault {
	return contract.NewFault(contract.CodeContractSchemaUnsupported, contract.ExitFailure,
		fmt.Sprintf("project contract %s declares schema %d; this igdev speaks schema %d and will not read the file partially",
			in.ContractPath, declared, project.LatestSchema)).
		WithRemediation(
			contract.Remediation{
				Command: "igdev init",
				Why:     "rewrite igdev.toml at the supported schema layout (the diff shows what changes)",
			},
			contract.Remediation{
				Command: buildinfo.InstallCommand,
				Why:     "or install a newer igdev that speaks this contract schema",
			})
}

func stampFault(in Input, digest string, schema int) *contract.Fault {
	remediation := contract.Remediation{
		Command: "igdev setup",
		Why:     "materialize this checkout from the Project Contract",
	}
	if !in.SetupPresent {
		return contract.NewFault(contract.CodeSetupRequired, contract.ExitFailure,
			fmt.Sprintf("no Checkout Setup for this project root: %s does not exist", in.SetupPath)).
			WithRemediation(remediation)
	}
	return contract.NewFault(contract.CodeSetupStale, contract.ExitFailure,
		fmt.Sprintf("Checkout Setup in %s is stale: %s", in.SetupPath, staleReason(in, digest, schema))).
		WithRemediation(remediation)
}

// minVersionFault applies the contract's own floor on the CLI. A version that
// does not parse — a development build, say — cannot be compared, so it is not
// a reason to refuse to run.
func minVersionFault(in Input, doc project.Doc) *contract.Fault {
	if doc.Tool.MinVersion == "" {
		return nil
	}
	want, err := semver.Parse(doc.Tool.MinVersion)
	if err != nil {
		return nil
	}
	have, err := semver.Parse(in.CLIVersion)
	if err != nil {
		return nil
	}
	if !have.Less(want) {
		return nil
	}
	return contract.NewFault(contract.CodeVersionUnsupported, contract.ExitFailure,
		fmt.Sprintf("this project requires igdev >= %s; this binary is %s", doc.Tool.MinVersion, in.CLIVersion)).
		WithRemediation(
			contract.Remediation{
				Command: buildinfo.InstallCommand,
				Why:     "install a newer igdev",
			},
			contract.Remediation{
				Command: "igdev version",
				Why:     "report the CLI version this checkout is running",
			})
}
