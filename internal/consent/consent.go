// Package consent implements the machine-global, human-only Consent record
// (ADR 0004): which legal terms this user and machine have accepted, when, and
// by which igdev version.
//
// Consent is user state, not project state, so the record lives in the igdev
// XDG config directory and is shared by every checkout of every project. Only a
// human-invoked command writes it: an automated run that meets a missing term
// stops with IGDEV_E_CONSENT_REQUIRED (exit level 3) and the exact command a
// person has to run.
package consent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
)

// FileName is the record's name inside the igdev XDG config directory.
const FileName = "accepted.toml"

// Path is the machine-global record's path inside a config directory.
func Path(configDir string) string { return filepath.Join(configDir, FileName) }

// Term is a legal term a human must accept. The ids are frozen: they are the
// section names of accepted.toml and appear in `igdev status --json`.
type Term struct {
	// ID is the frozen term id.
	ID string
	// Title names the term in human-readable messages.
	Title string
	// Flag is the `igdev setup` flag a human passes to accept this term. It is
	// what remediation names, so an agent never has to guess.
	Flag string
	// Why explains, in one clause, what accepting unblocks.
	Why string
}

// The frozen term table. Term ids and flags are part of the CLI Contract: they
// appear in the record, in status output, and in Remediation.
var (
	// EULA is the Ignition EULA. Every runtime-touching command needs it,
	// starting with `igdev setup`.
	EULA = Term{
		ID:    "ignition-eula",
		Title: "Ignition EULA",
		Flag:  "--accept-eula",
		Why:   "the Gateway image refuses to start without it",
	}
	// ModuleLicense covers the licenses of the modules a project enables. The
	// module commands (ticket 12) require it; the record already holds it so the
	// acceptance is machine-global from the start.
	ModuleLicense = Term{
		ID:    "module-license",
		Title: "module licenses",
		Flag:  "--accept-module-license",
		Why:   "an enabled module whose license is not accepted will not load",
	}
	// ModuleCert covers the certificates of the modules a project enables.
	ModuleCert = Term{
		ID:    "module-cert",
		Title: "module certificates",
		Flag:  "--accept-module-certificate",
		Why:   "an enabled module whose certificate is not accepted will not load",
	}
)

// Terms lists every term in the frozen order they are checked, accepted, and
// rendered.
func Terms() []Term { return []Term{EULA, ModuleLicense, ModuleCert} }

// TermByID finds a term by its frozen id.
func TermByID(id string) (Term, bool) {
	for _, t := range Terms() {
		if t.ID == id {
			return t, true
		}
	}
	return Term{}, false
}

// Acceptance is one accepted term as recorded.
type Acceptance struct {
	// AcceptedAt is when a human accepted the term, RFC 3339 UTC.
	AcceptedAt string `toml:"accepted_at" json:"accepted_at"`
	// CLIVersion is the igdev version the human ran.
	CLIVersion string `toml:"cli_version" json:"cli_version"`
}

// Record is the whole machine-global record, keyed by term id. Only terms that
// were accepted appear.
type Record struct {
	Terms map[string]Acceptance
}

// Load reads the record at path. A missing file is an empty record and no
// error; a file that cannot be parsed returns the error, because an unreadable
// acceptance proves nothing. Callers that must not fail use the empty record.
func Load(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Record{Terms: map[string]Acceptance{}}, nil
	}
	if err != nil {
		return Record{Terms: map[string]Acceptance{}}, err
	}
	return Decode(raw)
}

// Decode parses record bytes.
func Decode(raw []byte) (Record, error) {
	rec := Record{Terms: map[string]Acceptance{}}
	if _, err := toml.Decode(string(raw), &rec.Terms); err != nil {
		return Record{Terms: map[string]Acceptance{}}, fmt.Errorf("consent record is not valid TOML: %w", err)
	}
	return rec, nil
}

// Accepted reports the recorded acceptance of one term.
func (r Record) Accepted(term Term) (Acceptance, bool) {
	a, ok := r.Terms[term.ID]
	return a, ok
}

// IDs lists the accepted term ids in the frozen term order.
func (r Record) IDs() []string {
	out := []string{}
	for _, term := range Terms() {
		if _, ok := r.Accepted(term); ok {
			out = append(out, term.ID)
		}
	}
	return out
}

// Accept returns r with term recorded. A term that is already accepted keeps its
// original entry: the first acceptance is the legal fact, so re-running the
// human command is a no-op write rather than a new timestamp.
func (r Record) Accept(term Term, at time.Time, cliVersion string) Record {
	out := Record{Terms: map[string]Acceptance{}}
	for id, a := range r.Terms {
		out.Terms[id] = a
	}
	if _, ok := out.Terms[term.ID]; ok {
		return out
	}
	out.Terms[term.ID] = Acceptance{
		AcceptedAt: at.UTC().Format(time.RFC3339),
		CLIVersion: cliVersion,
	}
	return out
}

// Encode renders the record as accepted.toml bytes. Only accepted terms appear,
// in the frozen order, so an unchanged record is byte-identical.
func (r Record) Encode() []byte {
	var b strings.Builder
	b.WriteString("# igdev Consent record (machine-global, ADR 0004).\n")
	b.WriteString("#\n")
	b.WriteString("# A human accepted these legal terms on this machine. The record is bound to\n")
	b.WriteString("# the user and the machine, not to a checkout, so a fresh worktree of an\n")
	b.WriteString("# already consented project needs no new ritual.\n")
	b.WriteString("#\n")
	b.WriteString("# Only a human-invoked command writes it: `igdev setup --accept-eula` (and the\n")
	b.WriteString("# later per-term flags). An automated run that finds a term missing stops with\n")
	b.WriteString("# IGDEV_E_CONSENT_REQUIRED at exit level 3 and prints the exact command.\n")
	for _, term := range Terms() {
		a, ok := r.Accepted(term)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n[%s]\n", term.ID)
		fmt.Fprintf(&b, "accepted_at = %s\n", tomlString(a.AcceptedAt))
		fmt.Fprintf(&b, "cli_version = %s\n", tomlString(a.CLIVersion))
	}
	return []byte(b.String())
}

// tomlString renders one TOML string value. The record holds timestamps and
// version numbers, so a quoted basic string is the whole grammar needed.
func tomlString(value string) string { return fmt.Sprintf("%q", value) }

// Write stores the record at path atomically with mode 0600, creating the config
// directory 0700. It reports whether the file changed.
func Write(path string, rec Record) (bool, error) {
	data := rec.Encode()
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return false, nil
	}
	if err := atomicfile.Write(path, data, 0o600, 0o700); err != nil {
		return false, fmt.Errorf("write consent record %s: %w", path, err)
	}
	return true, nil
}

// Accept records one term at path, creating the record when it does not exist.
// It reports whether the file changed; an already-accepted term is a no-op.
func Accept(path string, term Term, at time.Time, cliVersion string) (bool, error) {
	// A record igdev cannot parse carries no trustworthy acceptance, so the
	// human command rewrites it from what it is about to accept.
	rec, _ := Load(path)
	if _, ok := rec.Accepted(term); ok {
		return false, nil
	}
	return Write(path, rec.Accept(term, at, cliVersion))
}

// Check reports the first required term this machine has not accepted, as the
// human-required fault every runtime-touching command returns unchanged. Nil
// means every required term is accepted. A record that cannot be read is the
// same as one that is not there: nothing was proven.
func Check(path string, required ...Term) *contract.Fault {
	rec, _ := Load(path)
	for _, term := range required {
		if _, ok := rec.Accepted(term); !ok {
			return requiredFault(path, term)
		}
	}
	return nil
}

func requiredFault(path string, term Term) *contract.Fault {
	return contract.NewFault(contract.CodeConsentRequired, contract.ExitHumanAction,
		fmt.Sprintf("%s has not been accepted on this machine (Consent record: %s)", term.Title, path)).
		WithRemediation(contract.Remediation{
			Command: "igdev setup " + term.Flag,
			Why: "a person must accept " + term.Title + " once per machine: " + term.Why +
				" (agents may never accept on a human's behalf, ADR 0004)",
		})
}
