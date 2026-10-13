// Package interrupt is igdev's answer to SIGINT and SIGTERM. Go's default action
// for both ends the process at once, so no deferred cleanup runs: a Ctrl-C during
// `ci-local --with-gateway` would leave the files that carry the Instance API
// token behind, and one during a lookup index build its scratch directory and
// temporary container.
//
// Install replaces that default with one ordered exit. On the first signal the
// handler says so on stderr, runs every cleanup still registered with Defer, waits
// for the sections held with Hold (a direct child igdev is waiting on) to finish,
// runs any cleanup registered meanwhile, and then re-raises the same signal with
// its default action restored. The process therefore still dies by that signal: a
// shell sees the status it always saw (130 for SIGINT, 143 for SIGTERM), and an
// interrupted run prints no envelope, exactly as before. A second signal stops the
// wait and exits at once.
//
// The handler never signals a child itself. A terminal Ctrl-C already reaches the
// whole foreground process group; forwarding a parent-only SIGTERM, process-group
// isolation and timeouts are a separate, reviewed change (issue #149, phase 2).
package interrupt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// ErrInterrupted is what Hold's callers return when a signal already arrived: no
// new child starts and no new temporary state is created once the process is on
// its way out.
var ErrInterrupted = errors.New("interrupted: igdev is exiting")

// handler is the process-wide state. Signals are process-wide, so is it; tests
// build their own.
type handler struct {
	mu   sync.Mutex
	idle *sync.Cond
	// cleanups are the registered cleanups in registration order; a released one
	// is nil. The handler runs them newest first, as defer would.
	cleanups []*cleanup
	holds    int
	signal   os.Signal
}

type cleanup struct {
	once sync.Once
	fn   func()
}

func (c *cleanup) run() { c.once.Do(c.fn) }

func newHandler() *handler {
	h := &handler{}
	h.idle = sync.NewCond(&h.mu)
	return h
}

var std = newHandler()

// Defer registers fn to run if the process is interrupted and returns the
// release the caller defers instead: release runs fn once and unregisters it, so
// the normal path and the interrupt path never both run it. Registered after a
// signal arrived, fn runs at once.
func Defer(fn func()) (release func()) { return std.deferFn(fn) }

// Hold marks a section the interrupt path waits for before it exits — a direct
// child igdev started, or the creation of temporary state together with its
// Defer — and returns the release that ends it. ok is false once a signal has
// arrived: the caller must not start the section and returns ErrInterrupted.
func Hold() (release func(), ok bool) { return std.hold() }

// Run runs cmd as a held section: the interrupt path waits for the child to exit
// rather than leaving it running unobserved.
func Run(cmd *exec.Cmd) error {
	release, ok := Hold()
	if !ok {
		return ErrInterrupted
	}
	defer release()
	return cmd.Run()
}

// MkdirTemp creates a temporary directory exactly like os.MkdirTemp and
// registers its removal, so a signal between creation and the caller's own defer
// still removes it. remove is the release the caller defers.
func MkdirTemp(dir, pattern string) (path string, remove func(), err error) {
	release, ok := Hold()
	if !ok {
		return "", nil, ErrInterrupted
	}
	defer release()
	path, err = os.MkdirTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	return path, Defer(func() { _ = os.RemoveAll(path) }), nil
}

// Interrupted reports whether a signal has arrived.
func Interrupted() bool {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.signal != nil
}

// Settle is the command tree's last step. After a signal it never returns: the
// handler is cleaning up and will end the process by that signal, and the command
// that was interrupted must not report a result of its own meanwhile.
func Settle() {
	if Interrupted() {
		select {}
	}
}

// Install starts handling SIGINT and SIGTERM for the life of the process, writing
// its diagnostics to stderr. A signal the process was started with ignored (a
// background job's SIGINT) stays ignored.
func Install(stderr io.Writer) {
	signals := notify()
	if signals == nil {
		return
	}
	go std.watch(signals, stderr, reraise)
}

func (h *handler) deferFn(fn func()) func() {
	c := &cleanup{fn: fn}
	h.mu.Lock()
	interrupted := h.signal != nil
	h.cleanups = append(h.cleanups, c)
	index := len(h.cleanups) - 1
	h.mu.Unlock()
	if interrupted {
		c.run()
	}
	return func() {
		h.mu.Lock()
		if index < len(h.cleanups) && h.cleanups[index] == c {
			h.cleanups[index] = nil
		}
		h.mu.Unlock()
		c.run()
	}
}

func (h *handler) hold() (func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.signal != nil {
		return nil, false
	}
	h.holds++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.holds--
			if h.holds == 0 {
				h.idle.Broadcast()
			}
			h.mu.Unlock()
		})
	}, true
}

// runCleanups runs every registered cleanup, newest first.
func (h *handler) runCleanups() {
	h.mu.Lock()
	pending := make([]*cleanup, 0, len(h.cleanups))
	for i := len(h.cleanups) - 1; i >= 0; i-- {
		if c := h.cleanups[i]; c != nil {
			pending = append(pending, c)
		}
	}
	h.mu.Unlock()
	for _, c := range pending {
		c.run()
	}
}

// waitIdle blocks until no section is held.
func (h *handler) waitIdle() {
	h.mu.Lock()
	for h.holds > 0 {
		h.idle.Wait()
	}
	h.mu.Unlock()
}

// watch handles the first signal on signals and ends the process through exit.
func (h *handler) watch(signals <-chan os.Signal, stderr io.Writer, exit func(os.Signal)) {
	sig := <-signals
	h.mu.Lock()
	h.signal = sig
	h.mu.Unlock()
	fmt.Fprintf(stderr, "[igdev] interrupted by %s: cleaning up before exit\n", name(sig))

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runCleanups()
		h.mu.Lock()
		waiting := h.holds > 0
		h.mu.Unlock()
		if waiting {
			fmt.Fprintf(stderr, "[igdev] waiting for the running child process to exit; interrupt again to stop waiting\n")
		}
		h.waitIdle()
		// A section that was held when the signal came may have registered its
		// cleanup since.
		h.runCleanups()
	}()
	select {
	case <-done:
	case again := <-signals:
		fmt.Fprintf(stderr, "[igdev] interrupted again by %s: exiting without waiting\n", name(again))
	}
	exit(sig)
}

// name is the conventional name of a signal for a diagnostic.
func name(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return sig.String()
}
