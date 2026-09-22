package contract

import (
	"errors"
	"testing"
)

// The envelope is the frozen contract: key order, key set, and the rule that a
// success carries an empty code and an empty (never null) remediation list.
func TestEncodeIsStableAndComplete(t *testing.T) {
	raw, err := Encode(Success(map[string]any{"initialized": true}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := `{
  "ok": true,
  "contract": "1",
  "code": "",
  "message": "ok",
  "remediation": [],
  "data": {
    "initialized": true
  }
}
`
	if string(raw) != want {
		t.Errorf("encoded envelope =\n%s\nwant\n%s", raw, want)
	}
}

// An error with no explicit Remediation still renders an empty array, and the
// exit level comes from the fault.
func TestFailureRendersFault(t *testing.T) {
	f := UsageFault("unknown command \"stat\" for \"igdev\"",
		Remediation{Command: "igdev --help", Why: "list the available commands"})
	env := Failure(f)
	if env.Ok || env.Code != string(CodeUsage) || env.Contract != Version {
		t.Errorf("failure envelope = %+v", env)
	}
	if len(env.Remediation) != 1 || env.Remediation[0].Command != "igdev --help" {
		t.Errorf("remediation = %+v", env.Remediation)
	}
	if f.Exit != ExitUsage {
		t.Errorf("usage fault exit = %d, want %d", f.Exit, ExitUsage)
	}
}

// Anything that is not a fault is reported as an internal failure at exit 1, so
// a missing code never becomes a silent success.
func TestAsFaultDefaultsToInternal(t *testing.T) {
	plain := errors.New("boom")
	f := AsFault(plain)
	if f.Code != CodeInternal || f.Exit != ExitFailure {
		t.Errorf("AsFault(plain) = %+v", f)
	}
	wrapped := errors.Join(errors.New("outer"), f)
	if got := AsFault(wrapped); got.Code != CodeInternal {
		t.Errorf("AsFault through a join = %+v", got)
	}
}

// A fault keeps its identity through wrapping, and the cause stays out of the
// envelope so machine output never depends on a transient error string.
func TestFaultCauseStaysOutOfEnvelope(t *testing.T) {
	f := NewFault(CodeConfigInvalid, ExitFailure, "local config is not valid TOML").
		WithCause(errors.New("dangling file /tmp/x"))
	env := Failure(f)
	if env.Message != "local config is not valid TOML" {
		t.Errorf("message = %q, want only the fault's own text", env.Message)
	}
	if !errors.Is(f, f.Unwrap()) {
		t.Errorf("cause is not reachable through Unwrap")
	}
}
