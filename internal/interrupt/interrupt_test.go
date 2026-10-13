package interrupt

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a stderr the handler goroutine and the test both touch.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// watching starts h's handler and returns the signal feed, its stderr, and the
// signal it exited with once it does.
func watching(h *handler) (chan os.Signal, *syncBuffer, <-chan os.Signal) {
	signals := make(chan os.Signal, 2)
	stderr := &syncBuffer{}
	exited := make(chan os.Signal, 1)
	go h.watch(signals, stderr, func(sig os.Signal) { exited <- sig })
	return signals, stderr, exited
}

func waitExit(t *testing.T, exited <-chan os.Signal) os.Signal {
	t.Helper()
	select {
	case sig := <-exited:
		return sig
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never exited")
		return nil
	}
}

// The normal path runs a cleanup once and unregisters it, so an interrupt
// afterwards does not run it again.
func TestReleaseRunsOnceAndUnregisters(t *testing.T) {
	h := newHandler()
	runs := 0
	release := h.deferFn(func() { runs++ })
	release()
	release()
	signals, _, exited := watching(h)
	signals <- syscall.SIGINT
	waitExit(t, exited)
	if runs != 1 {
		t.Errorf("cleanup ran %d times, want 1", runs)
	}
}

// A signal that arrives while the normal path is still running a cleanup waits
// for that cleanup to finish before the process exits: a removal cut off midway
// leaves the state it was removing.
func TestSignalDuringReleaseWaitsForTheCleanup(t *testing.T) {
	h := newHandler()
	entered, unblock := make(chan struct{}), make(chan struct{})
	finished := false
	release := h.deferFn(func() {
		close(entered)
		<-unblock
		finished = true
	})
	released := make(chan struct{})
	go func() {
		defer close(released)
		release()
	}()
	<-entered
	signals, _, exited := watching(h)
	signals <- syscall.SIGTERM
	select {
	case <-exited:
		t.Fatal("the handler exited while the normal path was still running a cleanup")
	case <-time.After(100 * time.Millisecond):
	}
	close(unblock)
	if got := waitExit(t, exited); got != syscall.SIGTERM {
		t.Errorf("exited by %v, want SIGTERM", got)
	}
	<-released
	if !finished {
		t.Error("the handler exited before the cleanup finished")
	}
}

// A signal runs every registered cleanup, newest first, then exits by the same
// signal and says so on stderr.
func TestSignalRunsCleanupsNewestFirst(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		h := newHandler()
		var order []string
		h.deferFn(func() { order = append(order, "first") })
		h.deferFn(func() { order = append(order, "second") })
		signals, stderr, exited := watching(h)
		signals <- sig
		if got := waitExit(t, exited); got != sig {
			t.Errorf("exited by %v, want %v", got, sig)
		}
		if strings.Join(order, ",") != "second,first" {
			t.Errorf("cleanup order = %v", order)
		}
		want := "[igdev] interrupted by " + name(sig) + ": cleaning up before exit\n"
		if stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

// The handler cleans up first, then waits for a held child, then runs what the
// held section registered meanwhile; no new section starts after the signal.
func TestSignalCleansUpBeforeWaitingForAHeldChild(t *testing.T) {
	h := newHandler()
	cleaned := make(chan struct{})
	h.deferFn(func() { close(cleaned) })
	release, ok := h.hold()
	if !ok {
		t.Fatal("hold refused before any signal")
	}
	signals, stderr, exited := watching(h)
	signals <- syscall.SIGTERM

	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("the cleanup did not run while the child was still held")
	}
	select {
	case <-exited:
		t.Fatal("the handler exited while a child was still held")
	case <-time.After(100 * time.Millisecond):
	}
	if _, ok := h.hold(); ok {
		t.Error("a new section started after the signal")
	}
	late := false
	h.deferFn(func() { late = true })
	release()
	waitExit(t, exited)
	if !late {
		t.Error("a cleanup registered by the held section did not run")
	}
	if !strings.Contains(stderr.String(), "waiting for the running child process to exit") {
		t.Errorf("stderr does not say it is waiting: %q", stderr.String())
	}
}

// A second signal stops the wait for a child that does not exit.
func TestSecondSignalStopsTheWait(t *testing.T) {
	h := newHandler()
	if _, ok := h.hold(); !ok {
		t.Fatal("hold refused")
	}
	signals, stderr, exited := watching(h)
	signals <- syscall.SIGTERM
	time.Sleep(50 * time.Millisecond)
	signals <- syscall.SIGINT
	if got := waitExit(t, exited); got != syscall.SIGTERM {
		t.Errorf("exited by %v, want the first signal SIGTERM", got)
	}
	if !strings.Contains(stderr.String(), "[igdev] interrupted again by SIGINT: exiting without waiting") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// Without a signal nothing changes: a held section runs, a cleanup waits for its
// release, and Settle returns.
func TestNoSignalLeavesTheNormalPathAlone(t *testing.T) {
	h := newHandler()
	ran := false
	release := h.deferFn(func() { ran = true })
	held, ok := h.hold()
	if !ok {
		t.Fatal("hold refused")
	}
	held()
	if ran {
		t.Error("a cleanup ran before its release")
	}
	release()
	if !ran {
		t.Error("release did not run the cleanup")
	}
	Settle()
}

func TestMkdirTempRemovesOnRelease(t *testing.T) {
	dir, remove, err := MkdirTemp(t.TempDir(), "igdev-test-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("not created: %v", err)
	}
	remove()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s is still there after release: %v", dir, err)
	}
}
