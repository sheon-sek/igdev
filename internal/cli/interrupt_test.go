package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/interrupt"
)

// stdoutSeen copies to the real stdout and closes seen on the first write.
type stdoutSeen struct{ seen chan struct{} }

func (w stdoutSeen) Write(p []byte) (int, error) {
	n, err := os.Stdout.Write(p)
	select {
	case <-w.seen:
	default:
		close(w.seen)
	}
	return n, err
}

// A parent-only SIGTERM leaves act to finish, and it can finish with 0 while the
// interrupt cleanup is still running. The interrupted run must still print no
// envelope: the process dies by the signal with nothing on stdout.
func TestInterruptedRunEmitsNoSuccess(t *testing.T) {
	if os.Getenv("IGDEV_TEST_INTERRUPT_HELPER") == "1" {
		interruptedSuccessHelper(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptedRunEmitsNoSuccess$")
	cmd.Env = append(os.Environ(), "IGDEV_TEST_INTERRUPT_HELPER=1")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the helper did not fail: %v", err)
	}
	status, _ := exitErr.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("the helper ended with %v, want death by SIGTERM", err)
	}
	if stdout.Len() > 0 {
		t.Errorf("an interrupted run printed on stdout: %q", stdout.String())
	}
}

// interruptedSuccessHelper runs act (a shell that sleeps and exits 0) under a
// SIGTERM whose cleanup stays pending until stdout is written or two seconds
// pass, whichever is first.
func interruptedSuccessHelper(t *testing.T) {
	interrupt.Install(io.Discard)
	seen := make(chan struct{})
	interrupt.Defer(func() {
		select {
		case <-seen:
		case <-time.After(2 * time.Second):
		}
	})
	app := New(stdoutSeen{seen}, io.Discard, nil)
	res, err := config.Resolve(config.Input{Flags: map[string]string{"output.format": "json"}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	if err := app.runAct(res, "/bin/sh", ciLocalData{Args: []string{"act", "-c", "sleep 0.5"}}); err != nil {
		t.Fatal(err)
	}
	interrupt.Settle()
}
