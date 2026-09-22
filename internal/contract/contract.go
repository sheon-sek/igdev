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
	// IGDEV_E_CONSENT_REQUIRED is the first code that emits it: an agent stops
	// and hands off rather than paraphrasing the error.
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
	// Re-materializing the checkout is `igdev setup`.
	CodeSetupStale Code = "IGDEV_E_SETUP_STALE"
	// CodeConsentRequired covers a legal term this machine has not accepted.
	// It is the human-required code: exit level 3, with the exact accept
	// command in Remediation, because only a person may accept a term (ADR 0004).
	CodeConsentRequired Code = "IGDEV_E_CONSENT_REQUIRED"
	// CodePortAlloc covers a failed dynamic port allocation: the host refused
	// three loopback binds, so the Instance has no ports to record (ADR 0003).
	CodePortAlloc Code = "IGDEV_E_PORT_ALLOC"
	// CodeContractSchemaUnsupported covers a Project Contract declaring a
	// schema version this binary does not speak. It fails closed: the file is
	// never partially parsed and never auto-downgraded.
	CodeContractSchemaUnsupported Code = "IGDEV_E_CONTRACT_SCHEMA_UNSUPPORTED"
	// CodeVersionUnsupported covers a Project Contract requiring a newer igdev
	// than the one running: `[tool].min_version` is above this binary.
	CodeVersionUnsupported Code = "IGDEV_E_VERSION_UNSUPPORTED"
	// CodeCapacity covers the Capacity Gate refusing to start a Gateway because
	// the machine's free memory is below the requested heap plus headroom
	// (ADR 0003). It is a human-required code: exit level 3, because a person
	// either frees memory or accepts the risk with --force.
	CodeCapacity Code = "IGDEV_E_CAPACITY"
	// CodeGatewayUnhealthy covers a Gateway that did not answer health checks
	// before the deadline, or answered a smoke check with an error status.
	CodeGatewayUnhealthy Code = "IGDEV_E_GATEWAY_UNHEALTHY"
	// CodeDocker covers a container-engine call that failed: the engine is
	// missing, is not compose v2, or refused the work.
	CodeDocker Code = "IGDEV_E_DOCKER"
	// CodeBaselineMissing covers a `baseline set` source path that does not
	// exist: there is nothing to stage. The remediation names the argument.
	CodeBaselineMissing Code = "IGDEV_E_BASELINE_MISSING"
	// CodeBaselineInvalid covers a `baseline set` source that exists but cannot
	// be staged as a backup file: a directory, or a path that cannot be read.
	CodeBaselineInvalid Code = "IGDEV_E_BASELINE_INVALID"
	// CodeUnknownCapability covers a capability the Effective Catalog cannot
	// resolve: a system.* function that is not a Gateway-scope 8.3 native
	// function, a REST request absent from the version's catalog, or a name no
	// capability rule maps. It is the preflight answer "nothing owns this".
	CodeUnknownCapability Code = "IGDEV_E_UNKNOWN_CAPABILITY"
	// CodeModuleNotEnabled covers a required module outside the module
	// whitelist: scanning says the code needs an artifact the Gateway will not
	// load. Remediation names the enabling command.
	CodeModuleNotEnabled Code = "IGDEV_E_MODULE_NOT_ENABLED"
	// CodeModuleArtifactMissing covers an enabled module that is neither a
	// built-in nor backed by a `.modl` artifact in the checkout: the whitelist
	// asks for a module nothing can load.
	CodeModuleArtifactMissing Code = "IGDEV_E_MODULE_ARTIFACT_MISSING"
	// CodeModuleArchiveInvalid covers a `.modl` that cannot be staged: not a
	// readable archive, no module.xml, no usable module id, or an archive whose
	// declared expansion trips the zip-bomb guard. The archive is never treated
	// as a capability source (ADR 0005), so the fault is about the file, not
	// about what it might contain.
	CodeModuleArchiveInvalid Code = "IGDEV_E_MODULE_ARCHIVE_INVALID"
	// CodeModuleUnknown covers `module enable` being asked for an id that is
	// neither a built-in module nor declared by a staged `.modl`: enabling it
	// would whitelist a module nothing can load. The message names the closest
	// built-in ids, because a typo is the common cause.
	CodeModuleUnknown Code = "IGDEV_E_MODULE_UNKNOWN"
	// CodeOverlayInvalid covers a Project Overlay file the contract declares
	// that cannot be read or does not follow the overlay format.
	CodeOverlayInvalid Code = "IGDEV_E_OVERLAY_INVALID"
	// CodeOverlayConflict covers a Project Overlay whose row has the same key as
	// a Core Catalog row. The overlay may add to the Core Catalog and may not
	// shadow it silently, so the run stops and names both rows.
	CodeOverlayConflict Code = "IGDEV_E_OVERLAY_CONFLICT"
	// CodeCatalogVersionMissing covers an Ignition version this binary carries
	// no Core Catalog for. Knowledge fails closed; there is no guessing at
	// another version's data.
	CodeCatalogVersionMissing Code = "IGDEV_E_CATALOG_VERSION_MISSING"
	// CodeCapabilityAmbiguous covers one capability resolving to more than one
	// owner. Preflight cannot pick for the caller, so it names the candidates.
	CodeCapabilityAmbiguous Code = "IGDEV_E_CAPABILITY_AMBIGUOUS"
	// CodeJavaMissing covers a Jython check with no JVM on PATH: the batched
	// compatibility compile cannot run without one.
	CodeJavaMissing Code = "IGDEV_E_JAVA_MISSING"
	// CodeJythonVersionUnsupported covers a contract naming a Jython version
	// this binary carries no pinned artifact digest for. The cache fails closed:
	// an unverifiable download is never trusted.
	CodeJythonVersionUnsupported Code = "IGDEV_E_JYTHON_VERSION_UNSUPPORTED"
	// CodeJythonFetch covers the pinned Jython artifact that could not be
	// downloaded: no network, a failed request, or an unwritable cache.
	CodeJythonFetch Code = "IGDEV_E_JYTHON_FETCH"
	// CodeChecksumMismatch covers a downloaded artifact whose sha256 does not
	// match the pin, after the bad bytes were quarantined and the fetch retried
	// once. It is the integrity failure: the artifact is never used.
	CodeChecksumMismatch Code = "IGDEV_E_CHECKSUM_MISMATCH"
	// CodeJythonPathMissing covers a `jython check` argument that does not
	// exist: there is nothing to compile.
	CodeJythonPathMissing Code = "IGDEV_E_JYTHON_PATH_MISSING"
	// CodeJythonSyntax covers files the batched JVM compile rejected: the
	// message names each path and line. It is what `check` reports for the
	// Jython stage.
	CodeJythonSyntax Code = "IGDEV_E_JYTHON_SYNTAX"
	// CodeCommandFailed covers a declared project stage (`[commands].check`,
	// `.test`, `.build`) that exited non-zero. The exit level is the stage's own
	// exit code, so a project's failure stays machine-distinguishable.
	CodeCommandFailed Code = "IGDEV_E_COMMAND_FAILED"
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
// reported as IGDEV_E_INTERNAL at exit 1. A fault carrying Data reports it; every
// other fault reports the frozen empty object.
func Failure(err error) Envelope {
	f := AsFault(err)
	remediation := f.Remediation
	if remediation == nil {
		remediation = []Remediation{}
	}
	data := f.Data
	if data == nil {
		data = map[string]any{}
	}
	return Envelope{
		Ok:          false,
		Contract:    Version,
		Code:        string(f.Code),
		Message:     f.Message,
		Remediation: remediation,
		Data:        data,
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
