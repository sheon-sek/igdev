package testrig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandResult is the observable outcome of running a helper program (the
// packaging script, an installed binary) rather than igdev itself.
type CommandResult struct {
	Stdout string
	Stderr string
	Exit   int
}

// Combined is stdout plus stderr, for error messages.
func (r CommandResult) Combined() string {
	return strings.TrimRight(r.Stdout+"\n"+r.Stderr, "\n")
}

// Command runs name with args in dir, inheriting the test process environment
// plus extraEnv. Where Env.Run isolates, Command must not: build and packaging
// tools need the real toolchain, HOME, and PATH.
func Command(dir string, extraEnv []string, name string, args ...string) (CommandResult, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	res := CommandResult{Stdout: out.String(), Stderr: errBuf.String()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
}

// RunScriptIn runs a script from the repository and fails when it exits
// non-zero, reporting its own output.
func RunScriptIn(dir string, extraEnv []string, script string, args ...string) error {
	res, err := Command(dir, extraEnv, script, args...)
	if err != nil {
		return err
	}
	if res.Exit != 0 {
		return fmt.Errorf("%s exited %d\n%s", script, res.Exit, res.Combined())
	}
	return nil
}

// OutputOf runs a built binary and returns its stdout, failing on a non-zero
// exit. It is how the release test proves an installed artifact really runs.
func OutputOf(path string, args ...string) (string, error) {
	res, err := Command("", nil, path, args...)
	if err != nil {
		return "", err
	}
	if res.Exit != 0 {
		return "", fmt.Errorf("%s exited %d\n%s", path, res.Exit, res.Combined())
	}
	return res.Stdout, nil
}
