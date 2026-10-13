package testrig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Proc is one invocation of the binary running in the background, so a test can
// signal it mid-run. It leads its own process group: a signal to the group is
// what a terminal's Ctrl-C delivers (igdev and every child it started), a signal
// to the process alone is what a supervisor's SIGTERM delivers.
type Proc struct {
	t       *testing.T
	cmd     *exec.Cmd
	out     bytes.Buffer
	errBuf  bytes.Buffer
	started time.Time
	res     Result
}

// Start launches the binary in the background. Wait collects it.
func (e *Env) Start(r Run) *Proc {
	e.T.Helper()
	dir := r.Dir
	if dir == "" {
		dir = e.Dir
	}
	p := &Proc{t: e.T}
	p.cmd = exec.Command(e.Binary, r.Args...)
	p.cmd.Dir = dir
	p.cmd.Env = e.EnvOverlay(r.Env...)
	p.cmd.Stdout = &p.out
	p.cmd.Stderr = &p.errBuf
	if r.Stderr != nil {
		p.cmd.Stderr = r.Stderr
	}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.res = Result{env: e, before: e.Snapshot()}
	p.started = time.Now()
	if err := p.cmd.Start(); err != nil {
		e.T.Fatalf("start %s: %v", e.Binary, err)
	}
	// A child igdev left running (a second signal stops the wait for it) is still
	// in the group: kill it before the scratch tree is removed.
	e.T.Cleanup(func() {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Wait()
		}
	})
	return p
}

// Signal sends sig to igdev alone; its children do not receive it.
func (p *Proc) Signal(sig syscall.Signal) {
	p.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.t.Fatalf("signal %v: %v", sig, err)
	}
}

// SignalGroup sends sig to igdev's whole process group, as a terminal does.
func (p *Proc) SignalGroup(sig syscall.Signal) {
	p.t.Helper()
	if err := syscall.Kill(-p.cmd.Process.Pid, sig); err != nil {
		p.t.Fatalf("signal group %v: %v", sig, err)
	}
}

// Wait collects the process. A process that is still running after timeout is
// killed and fails the test.
func (p *Proc) Wait(timeout time.Duration) Result {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(timeout):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-done
		p.t.Fatalf("igdev still running after %v\nstdout: %s\nstderr: %s", timeout, p.out.String(), p.errBuf.String())
	}
	res := p.res
	res.Duration = time.Since(p.started)
	res.Stdout, res.Stderr = p.out.String(), p.errBuf.String()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		res.Err = fmt.Errorf("wait: %w", waitErr)
		return res
	}
	res.Exit = p.cmd.ProcessState.ExitCode()
	if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		res.Signal = status.Signal()
	}
	return res
}

// WaitForFile polls until path exists, failing the test after timeout.
func WaitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within %v", path, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
