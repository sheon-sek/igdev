package jython

import (
	"bytes"
	_ "embed"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// driverSource is the batch driver handed to the JVM with -c. It is one Jython
// process compiling every file it is given, which is what turns N launches into
// one.
//
//go:embed assets/check.py
var driverSource string

// Diagnostic is one file the batched compile rejected.
type Diagnostic struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// String renders the diagnostic the way the driver printed it: `path:line: why`,
// so an agent can fix the file without reading a second field.
func (d Diagnostic) String() string {
	switch {
	case d.File == "":
		return d.Message
	case d.Line > 0:
		return fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Message)
	default:
		return fmt.Sprintf("%s: %s", d.File, d.Message)
	}
}

// diagnosticRE matches the driver's `path:line: message` line. The path is
// greedy so a path containing colons keeps its own.
var diagnosticRE = regexp.MustCompile(`^(.*):([0-9]+): (.*)$`)

// Compile runs one JVM over every file and reports the files it rejected. An
// empty file list compiles nothing and launches nothing.
//
// The returned fault is IGDEV_E_JYTHON_SYNTAX and names each failing file with
// its line; the diagnostics are the machine-readable form of the same thing.
func Compile(spec Spec, jar string, files []string) ([]Diagnostic, *contract.Fault) {
	if len(files) == 0 {
		return nil, nil
	}
	java, err := exec.LookPath("java")
	if err != nil {
		return nil, contract.NewFault(contract.CodeJavaMissing, contract.ExitFailure,
			"the Jython check needs a JVM: java is not on PATH").WithCause(err).
			WithRemediation(contract.Remediation{
				Command: "igdev doctor",
				Why:     "audit the toolchain prerequisites igdev depends on",
			})
	}
	args := make([]string, 0, len(files)+4)
	args = append(args, "-jar", jar, "-c", driverSource)
	args = append(args, files...)

	cmd := exec.Command(java, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()

	diagnostics := parseDiagnostics(output.String())
	if runErr == nil {
		return nil, nil
	}
	if len(diagnostics) == 0 {
		return nil, contract.NewFault(contract.CodeJythonSyntax, contract.ExitFailure,
			fmt.Sprintf("the Jython check failed: %s", strings.TrimSpace(output.String()))).WithCause(runErr)
	}
	problems := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		problems = append(problems, d.String())
	}
	return diagnostics, contract.NewFault(contract.CodeJythonSyntax, contract.ExitFailure,
		strings.Join(problems, "; ")).WithCause(runErr)
}

// parseDiagnostics reads the driver's stderr into structured diagnostics. A line
// that does not follow the `path:line: message` shape is kept verbatim rather
// than dropped, so a JVM-level failure still reaches the caller.
func parseDiagnostics(output string) []Diagnostic {
	var out []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := diagnosticRE.FindStringSubmatch(line)
		if match == nil {
			out = append(out, Diagnostic{Message: line})
			continue
		}
		number, err := strconv.Atoi(match[2])
		if err != nil {
			out = append(out, Diagnostic{Message: line})
			continue
		}
		out = append(out, Diagnostic{File: match[1], Line: number, Message: match[3]})
	}
	return out
}
