// Package testrig is the hermetic test rig for seam S1: the compiled binary's
// process boundary. Every igdev behaviour test runs the real binary with a
// controlled environment — a throwaway HOME, a PATH shim for docker, a loopback
// HTTP server standing in for the releases endpoint — and asserts only
// observable behaviour: exit level, stdout, stderr, filesystem effects, and the
// shim's call log.
//
// The rig deliberately exposes no in-process hooks: what a test observes is what
// an agent or a shell observes.
package testrig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Env is one scratch machine: an isolated HOME tree, a shim PATH, a private temp
// dir, and the built igdev binary.
type Env struct {
	T *testing.T

	// Root is the scratch tree. Everything a run may touch lives under it, which
	// is what makes the leak assertion possible.
	Root string
	// Home is the child's HOME. XDG variables stay unset so runs exercise
	// igdev's real ~/.cache, ~/.config, ~/.local/state defaults.
	Home string
	// Temp is the child's TMPDIR.
	Temp string
	// Shim is prepended to PATH and holds the fake docker.
	Shim string
	// Dir is the default working directory for runs.
	Dir string
	// Binary is the built igdev under test.
	Binary string

	baseEnv      []string
	replacements []replacement
}

// NewEnv prepares an isolated scratch environment. The caller adds fixtures and
// runs commands through it; t.Cleanup removes the tree.
func NewEnv(t *testing.T) *Env {
	t.Helper()
	root := t.TempDir()
	env := &Env{
		T:      t,
		Root:   root,
		Home:   filepath.Join(root, "home"),
		Temp:   filepath.Join(root, "tmp"),
		Shim:   filepath.Join(root, "shim"),
		Binary: Binary(t),
	}
	for _, dir := range []string{env.Home, env.Temp, env.Shim} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	env.Dir = env.Home
	env.baseEnv = []string{
		"HOME=" + env.Home,
		"TMPDIR=" + env.Temp,
		"PATH=" + env.Shim + string(os.PathListSeparator) + GoBinDir() +
			string(os.PathListSeparator) + strings.Join(SystemPATHList(), string(os.PathListSeparator)),
		"USER=igdev-test",
		"LANG=C",
		"LC_ALL=C",
		"SHELL=/bin/sh",
		// Default the update notice off: a test only reaches the network when it
		// opts in.
		"IGDEV_NO_UPDATE_NOTIFIER=1",
	}
	return env
}

// SystemPATHList is the minimal real PATH the rig itself needs: curl, tar, and
// sha256sum for the release tests, /bin/sh for everything.
func SystemPATHList() []string {
	parts := []string{}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			parts = append(parts, dir)
		}
	}
	return parts
}

// GoBinDir is the directory holding the go toolchain that built the binary under
// test, so a test can also have the scratch environment run `go build`.
func GoBinDir() string {
	bin, err := exec.LookPath("go")
	if err != nil {
		return "/usr/local/go/bin"
	}
	return filepath.Dir(bin)
}

// EnvOverlay returns the base environment with extra KEY=VALUE pairs applied,
// dropping any earlier assignment of the same name.
func (e *Env) EnvOverlay(extra ...string) []string {
	out := make([]string, 0, len(e.baseEnv)+len(extra))
	applied := map[string]bool{}
	for _, pair := range extra {
		if name, _, ok := strings.Cut(pair, "="); ok {
			applied[name] = true
		}
	}
	for _, pair := range e.baseEnv {
		if name, _, ok := strings.Cut(pair, "="); ok && applied[name] {
			continue
		}
		out = append(out, pair)
	}
	return append(out, extra...)
}

// SetBaseEnv replaces or adds a base environment entry for every later run in
// this Env. The update-notice tests use it to clear IGDEV_NO_UPDATE_NOTIFIER.
func (e *Env) SetBaseEnv(pair string) {
	name, _, _ := strings.Cut(pair, "=")
	filtered := make([]string, 0, len(e.baseEnv)+1)
	for _, existing := range e.baseEnv {
		if existingName, _, _ := strings.Cut(existing, "="); existingName == name {
			continue
		}
		filtered = append(filtered, existing)
	}
	e.baseEnv = append(filtered, pair)
}

// Run describes one invocation of the binary.
type Run struct {
	Args []string
	// Dir is the working directory; defaults to Env.Dir.
	Dir string
	// Env holds extra KEY=VALUE pairs applied over the base environment.
	Env []string
	// Stdin is piped to the child when non-empty.
	Stdin string
	// SampleRSS polls /proc for the child's peak resident set while it runs.
	SampleRSS bool
}

// Result is everything an outside observer can see of one invocation.
type Result struct {
	Exit     int
	Stdout   string
	Stderr   string
	Duration time.Duration
	// Screen is what an interactive session showed, rendered as text: an
	// interactive run puts both streams on one terminal, so the transcript is
	// what both dialects printed. It is empty for a non-interactive run.
	Screen string
	// PeakRSSKB is the largest VmHWM observed for the child, in KiB. Zero means
	// no sample was caught.
	PeakRSSKB int64
	// Samples counts the successful /proc reads.
	Samples int
	// Err is a transport-level failure. The binary exiting non-zero is data,
	// not an error.
	Err error

	// env and before record the scratch tree as it stood when the process
	// started, so a leak assertion needs no bookkeeping from the test.
	env    *Env
	before Snapshot
}

// Run executes the binary and returns the observed result.
func (e *Env) Run(r Run) Result {
	e.T.Helper()
	dir := r.Dir
	if dir == "" {
		dir = e.Dir
	}
	var out, errBuf bytes.Buffer
	cmd := exec.Command(e.Binary, r.Args...)
	cmd.Dir = dir
	cmd.Env = e.EnvOverlay(r.Env...)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if r.Stdin != "" {
		cmd.Stdin = strings.NewReader(r.Stdin)
	}

	res := Result{env: e, before: e.Snapshot()}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		res.Err = fmt.Errorf("start %s: %w", e.Binary, err)
		return res
	}
	collect := func() []sample { return nil }
	if r.SampleRSS {
		collect = startRSSPoll(cmd.Process.Pid)
	}
	waitErr := cmd.Wait()
	res.Duration = time.Since(started)
	for _, s := range collect() {
		res.Samples++
		if s.peakKB > res.PeakRSSKB {
			res.PeakRSSKB = s.peakKB
		}
	}
	res.Stdout = out.String()
	res.Stderr = errBuf.String()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			res.Err = fmt.Errorf("run %v: %w", r.Args, waitErr)
			return res
		}
		res.Exit = exitErr.ExitCode()
	}
	return res
}

// MustRun runs args and fails the test if the process could not be launched.
func (e *Env) MustRun(args ...string) Result {
	e.T.Helper()
	res := e.Run(Run{Args: args})
	e.check(res)
	return res
}

// RunIn runs args inside dir.
func (e *Env) RunIn(dir string, args ...string) Result {
	e.T.Helper()
	res := e.Run(Run{Args: args, Dir: dir})
	e.check(res)
	return res
}

func (e *Env) check(res Result) {
	e.T.Helper()
	if res.Err != nil {
		e.T.Fatalf("%v\nstdout: %s\nstderr: %s", res.Err, res.Stdout, res.Stderr)
	}
}

// RunShell runs a command through /bin/sh with the scratch environment in the
// environment's default directory.
func (e *Env) RunShell(script string, extraEnv ...string) Result {
	e.T.Helper()
	return e.RunShellIn(e.Home, script, extraEnv...)
}

// RunShellIn runs a command through /bin/sh in dir with the scratch environment;
// the release tests use it to invoke the packaging and installer scripts.
func (e *Env) RunShellIn(dir, script string, extraEnv ...string) Result {
	e.T.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = e.EnvOverlay(extraEnv...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	started := time.Now()
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errBuf.String(), Duration: time.Since(started)}
	if err == nil {
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.Exit = exitErr.ExitCode()
		return res
	}
	res.Err = err
	return res
}
