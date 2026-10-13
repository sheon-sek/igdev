//go:build linux || darwin

package interrupt

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// notify subscribes to SIGINT and SIGTERM, leaving out one the process was
// started with ignored. nil means there is nothing to handle.
func notify() <-chan os.Signal {
	var handled []os.Signal
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		if !signal.Ignored(sig) {
			handled = append(handled, sig)
		}
	}
	if len(handled) == 0 {
		return nil
	}
	// Room for the first signal and a second one that stops the wait.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, handled...)
	return signals
}

// reraise ends the process by sig with its default action restored, so the
// parent observes a death by that signal, as it did before igdev handled it. The
// conventional 128+n exit is only the fallback if the signal does not land.
func reraise(sig os.Signal) {
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)
	n := sig.(syscall.Signal)
	_ = syscall.Kill(syscall.Getpid(), n)
	time.Sleep(time.Second)
	os.Exit(128 + int(n))
}
