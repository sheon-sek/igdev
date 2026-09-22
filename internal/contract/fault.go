package contract

import (
	"errors"
	"fmt"
)

// Fault is an error that already knows its contract code, exit level, and
// Remediation. Commands return faults; the CLI layer turns them into envelopes.
type Fault struct {
	Code        Code
	Message     string
	Exit        Exit
	Remediation []Remediation
	// Data is optional command-specific detail the failure envelope carries in
	// `data` instead of the empty object. The pipeline verbs use it to report the
	// per-stage results of the run that failed. A nil Data keeps the frozen empty
	// object every other fault reports.
	Data  any
	cause error
}

func (f *Fault) Error() string {
	if f.cause != nil {
		return fmt.Sprintf("%s: %s: %v", f.Code, f.Message, f.cause)
	}
	return fmt.Sprintf("%s: %s", f.Code, f.Message)
}

func (f *Fault) Unwrap() error { return f.cause }

// WithCause attaches the underlying failure. The cause never reaches the
// envelope: agents read Code, Message, and Remediation.
func (f *Fault) WithCause(err error) *Fault {
	out := *f
	out.cause = err
	return &out
}

// WithRemediation replaces the Remediation steps.
func (f *Fault) WithRemediation(r ...Remediation) *Fault {
	out := *f
	out.Remediation = r
	return &out
}

// WithData attaches command-specific detail the failure envelope reports in
// `data`. It is how a pipeline failure carries the per-stage results of the run
// that stopped.
func (f *Fault) WithData(data any) *Fault {
	out := *f
	out.Data = data
	return &out
}

// NewFault builds a fault. exit names the level the code belongs to.
func NewFault(code Code, exit Exit, message string) *Fault {
	return &Fault{Code: code, Message: message, Exit: exit}
}

// UsageFault reports a wrong invocation, naming what was wrong and pointing at
// help.
func UsageFault(message string, remediation ...Remediation) *Fault {
	return NewFault(CodeUsage, ExitUsage, message).WithRemediation(remediation...)
}

// AsFault maps any error onto a *Fault, defaulting to IGDEV_E_INTERNAL / exit 1.
func AsFault(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	return NewFault(CodeInternal, ExitFailure, fmt.Sprintf("igdev failed: %v", err))
}
