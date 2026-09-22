package consent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
)

var acceptedAt = time.Date(2026, 9, 23, 10, 11, 12, 0, time.UTC)

func recordPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "igdev", FileName)
}

// Nothing on record is not an error: a machine that has never consented simply
// has an empty record.
func TestLoadMissingRecordIsEmpty(t *testing.T) {
	rec, err := Load(recordPath(t))
	if err != nil {
		t.Fatalf("Load(missing) = %v, want no error", err)
	}
	if len(rec.Terms) != 0 {
		t.Errorf("Load(missing) = %+v, want an empty record", rec)
	}
	if ids := rec.IDs(); len(ids) != 0 {
		t.Errorf("IDs() = %v, want none", ids)
	}
}

// Accepting a term writes it with the timestamp and the CLI version, and
// whatever else was already recorded survives.
func TestAcceptRecordsTermsInFrozenOrder(t *testing.T) {
	path := recordPath(t)
	if changed, err := Accept(path, ModuleCert, acceptedAt, "0.1.0"); err != nil || !changed {
		t.Fatalf("Accept(module-cert) = %v, %v; want a change", changed, err)
	}
	if changed, err := Accept(path, EULA, acceptedAt, "0.1.0"); err != nil || !changed {
		t.Fatalf("Accept(ignition-eula) = %v, %v; want a change", changed, err)
	}

	rec, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := rec.IDs(); strings.Join(got, ",") != "ignition-eula,module-cert" {
		t.Errorf("IDs() = %v, want the frozen term order ignition-eula,module-cert", got)
	}
	accepted, ok := rec.Accepted(EULA)
	if !ok {
		t.Fatal("the EULA acceptance was not recorded")
	}
	if accepted.AcceptedAt != acceptedAt.Format(time.RFC3339) || accepted.CLIVersion != "0.1.0" {
		t.Errorf("recorded acceptance = %+v, want the timestamp and CLI version", accepted)
	}
	if _, ok := rec.Accepted(ModuleLicense); ok {
		t.Error("a term that was never accepted is recorded")
	}

	// The encoded file names each accepted term and nothing else.
	body := string(rec.Encode())
	for _, want := range []string{"[ignition-eula]", "[module-cert]", "accepted_at", "cli_version"} {
		if !strings.Contains(body, want) {
			t.Errorf("record does not carry %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[module-license]") {
		t.Errorf("record carries a term that was not accepted:\n%s", body)
	}
}

// Re-running the human command is a no-op write: the first acceptance is the
// legal fact, so the timestamp does not move and the file does not change.
func TestAcceptIsIdempotent(t *testing.T) {
	path := recordPath(t)
	if _, err := Accept(path, EULA, acceptedAt, "0.1.0"); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}

	later := acceptedAt.Add(48 * time.Hour)
	changed, err := Accept(path, EULA, later, "9.9.9")
	if err != nil {
		t.Fatalf("Accept(again): %v", err)
	}
	if changed {
		t.Error("Accept(again) reported a change")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("a second acceptance rewrote the record:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// The file is private: it records a legal statement by a person.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %o, want 600", info.Mode().Perm())
	}
}

// The missing-consent failure is the frozen human-required shape: exit level 3,
// its own code, and the exact command a person has to run.
func TestCheckReportsTheExactHumanCommand(t *testing.T) {
	path := recordPath(t)

	fault := Check(path, EULA)
	if fault == nil {
		t.Fatal("Check passed with no record at all")
	}
	if fault.Code != contract.CodeConsentRequired {
		t.Errorf("code = %s, want %s", fault.Code, contract.CodeConsentRequired)
	}
	if fault.Exit != contract.ExitHumanAction {
		t.Errorf("exit level = %d, want %d", fault.Exit, contract.ExitHumanAction)
	}
	if !strings.Contains(fault.Message, EULA.Title) {
		t.Errorf("message does not name the term: %q", fault.Message)
	}
	if len(fault.Remediation) != 1 || fault.Remediation[0].Command != "igdev setup --accept-eula" {
		t.Errorf("remediation = %+v, want the exact accept command", fault.Remediation)
	}

	if _, err := Accept(path, EULA, acceptedAt, "0.1.0"); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if fault := Check(path, EULA); fault != nil {
		t.Errorf("Check refused an accepted term: %v", fault)
	}
	// A term that was never accepted is still refused, and names its own flag.
	fault = Check(path, EULA, ModuleLicense)
	if fault == nil {
		t.Fatal("Check passed for an unaccepted required term")
	}
	if !strings.Contains(fault.Remediation[0].Command, ModuleLicense.Flag) {
		t.Errorf("remediation = %+v, want %s", fault.Remediation, ModuleLicense.Flag)
	}
}

// An unreadable record proves nothing: it is reported as missing consent, which
// is what a person then repairs with the accept command.
func TestUnreadableRecordIsNotConsent(t *testing.T) {
	path := recordPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("this is not [ toml"), 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Error("Load accepted a malformed record")
	}
	if fault := Check(path, EULA); fault == nil {
		t.Error("Check accepted a malformed record")
	}
	// The human command can still repair it, and starts the record over.
	if _, err := Accept(path, EULA, acceptedAt, "0.1.0"); err != nil {
		t.Fatalf("Accept over a malformed record: %v", err)
	}
	if fault := Check(path, EULA); fault != nil {
		t.Errorf("Check refused the repaired record: %v", fault)
	}
}

// Every term has a distinct id and a distinct accept flag; both are part of the
// CLI contract and appear in output.
func TestTermTableIsFrozen(t *testing.T) {
	want := map[string]string{
		"ignition-eula":  "--accept-eula",
		"module-license": "--accept-module-license",
		"module-cert":    "--accept-module-certificate",
	}
	ids := map[string]bool{}
	for _, term := range Terms() {
		if term.Flag != want[term.ID] {
			t.Errorf("term %s accepts with %q, want %q", term.ID, term.Flag, want[term.ID])
		}
		if ids[term.ID] {
			t.Errorf("term id %s appears twice", term.ID)
		}
		ids[term.ID] = true
		if term.Title == "" || term.Why == "" {
			t.Errorf("term %s has no title or rationale: %+v", term.ID, term)
		}
		if _, ok := TermByID(term.ID); !ok {
			t.Errorf("TermByID(%q) did not find the term", term.ID)
		}
	}
	if len(ids) != len(want) {
		t.Errorf("the term table has %d terms, want %d", len(ids), len(want))
	}
}
