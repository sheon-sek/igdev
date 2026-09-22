// Package contract holds the frozen machine-facing agent contract: the JSON
// envelope shape, the IGDEV_E_* error-code namespace, the exit levels, and the
// CLI Contract Version.
//
// Everything in this file is frozen by the golden tests in itest/. A change to
// an envelope key, a code, or an exit level is a break of the CLI Contract
// Version and must be recorded as an ADR.
package contract

import (
	"bytes"
	"encoding/json"
)

// Version is the CLI Contract Version: the stability epoch of the
// machine-facing interface (envelope keys, code namespace, exit levels).
// It is independent of the release semver of the binary.
const Version = "1"

// Exit is a process exit level. The four levels are the frozen vocabulary an
// agent uses to decide what to do next.
type Exit int

const (
	// ExitOK means the command succeeded.
	ExitOK Exit = 0
	// ExitFailure means the command ran correctly but failed; `code` says how.
	ExitFailure Exit = 1
	// ExitUsage means the invocation was wrong and cannot be attempted as given.
	ExitUsage Exit = 2
	// ExitHumanAction means a person must act (consent, destructive approval).
	// No ticket before 03 emits it; the level is frozen so agents can rely on it.
	ExitHumanAction Exit = 3
)

// Code is a namespaced machine-readable error code.
type Code string

const (
	// CodeOK is the empty code carried by a successful envelope.
	CodeOK Code = ""
	// CodeUsage covers an unknown command, an unknown flag, an argument the
	// command cannot use, and a --config override that names no known key.
	CodeUsage Code = "IGDEV_E_USAGE"
	// CodeMissingArgument covers a required argument that was not supplied.
	CodeMissingArgument Code = "IGDEV_E_MISSING_ARGUMENT"
	// CodeConfigInvalid covers a config tier the command read for itself —
	// environment, Project Contract, checkout-local config — that exists but
	// cannot be interpreted.
	CodeConfigInvalid Code = "IGDEV_E_CONFIG_INVALID"
	// CodeInternal covers an unexpected failure with no more specific code.
	CodeInternal Code = "IGDEV_E_INTERNAL"
	// CodeNotInitialized covers a project command run where no Project
	// Contract was found: there is no repository to act on yet.
	CodeNotInitialized Code = "IGDEV_E_NOT_INITIALIZED"
	// CodeSetupRequired covers a Project Contract that exists while the
	// Checkout Setup does not: the checkout has not been materialized.
	CodeSetupRequired Code = "IGDEV_E_SETUP_REQUIRED"
	// CodeSetupStale covers a Checkout Setup that no longer matches its
	// Project Contract (Contract Digest, schema, or CLI Contract Version).
	// Re-materializing the checkout is `igdev setup`; ticket 09 owns it.
	CodeSetupStale Code = "IGDEV_E_SETUP_STALE"
	// CodeContractSchemaUnsupported covers a Project Contract declaring a
	// schema version this binary does not speak. It fails closed: the file is
	// never partially parsed and never auto-downgraded.
	CodeContractSchemaUnsupported Code = "IGDEV_E_CONTRACT_SCHEMA_UNSUPPORTED"
	// CodeVersionUnsupported covers a Project Contract requiring a newer igdev
	// than the one running: `[tool].min_version` is above this binary.
	CodeVersionUnsupported Code = "IGDEV_E_VERSION_UNSUPPORTED"
)

// Remediation is a machine-readable next step: the exact command that clears
// the failure.
type Remediation struct {
	Command string `json:"command"`
	Why     string `json:"why"`
}

// Envelope is the single JSON shape every command emits. Field order is the
// frozen key order: ok, contract, code, message, remediation, data.
type Envelope struct {
	Ok          bool          `json:"ok"`
	Contract    string        `json:"contract"`
	Code        string        `json:"code"`
	Message     string        `json:"message"`
	Remediation []Remediation `json:"remediation"`
	Data        any           `json:"data"`
}

// MessageOK is the message carried by a successful envelope.
const MessageOK = "ok"

// Success builds a successful envelope. data must never be nil; callers with
// nothing to report pass an empty struct or map.
func Success(data any) Envelope {
	return Envelope{
		Ok:          true,
		Contract:    Version,
		Code:        string(CodeOK),
		Message:     MessageOK,
		Remediation: []Remediation{},
		Data:        data,
	}
}

// Failure builds an error envelope for err. An err that is not a *Fault is
// reported as IGDEV_E_INTERNAL at exit 1.
func Failure(err error) Envelope {
	f := AsFault(err)
	remediation := f.Remediation
	if remediation == nil {
		remediation = []Remediation{}
	}
	return Envelope{
		Ok:          false,
		Contract:    Version,
		Code:        string(f.Code),
		Message:     f.Message,
		Remediation: remediation,
		Data:        map[string]any{},
	}
}

// Encode renders an envelope as stable, indented JSON with a trailing newline.
// HTML escaping is off: codes, messages, and paths are data, not markup, so an
// agent can read `<shell>` without decoding \u003c.
func Encode(env Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
