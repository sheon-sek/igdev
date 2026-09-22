package testrig

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
)

// The frozen terminal a Wizard runs on. A Wizard's rendering and line wrapping
// depend on the terminal size, so a scripted session is only reproducible at a
// fixed one.
const (
	PTYRows = 40
	PTYCols = 120
	// ptyExpectTimeout bounds one wait for terminal output; ptyExitTimeout
	// bounds the whole session, so a Wizard that never exits fails the test
	// instead of hanging it.
	ptyExpectTimeout = 15 * time.Second
	ptyExitTimeout   = 30 * time.Second
	// ptyDrainDelay is how long the reader is given to collect the last output
	// after the process exits.
	ptyDrainDelay = 200 * time.Millisecond
)

// PTYRun describes one interactive invocation of the binary.
type PTYRun struct {
	Args []string
	// Dir is the working directory; defaults to Env.Dir.
	Dir string
	// Env holds extra KEY=VALUE pairs applied over the base environment.
	Env []string
}

// PTY is one interactive session: the binary's standard streams are a
// pseudo-terminal, so what a person would see is what the test reads and what a
// person would type is what the test sends. It is the only seam through which a
// Wizard can be exercised at all, because a Wizard exists only on a terminal.
type PTY struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File

	mu   sync.Mutex
	raw  []byte
	done chan struct{}
	res  Result
}

// StartPTY launches the binary on a fresh pseudo-terminal of the frozen size.
func (e *Env) StartPTY(r PTYRun) *PTY {
	e.T.Helper()
	dir := r.Dir
	if dir == "" {
		dir = e.Dir
	}
	cmd := exec.Command(e.Binary, r.Args...)
	cmd.Dir = dir
	cmd.Env = e.EnvOverlay(r.Env...)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: PTYRows, Cols: PTYCols})
	if err != nil {
		e.T.Fatalf("start %s on a pty: %v", e.Binary, err)
	}
	p := &PTY{
		t:      e.T,
		cmd:    cmd,
		master: master,
		done:   make(chan struct{}),
		res:    Result{env: e, before: e.Snapshot()},
	}
	e.T.Cleanup(p.close)
	read := make(chan struct{})
	go p.pump(read)
	go p.reap(read)
	return p
}

// Expect waits until the terminal has shown substr, and fails the test when it
// never does. Waiting for a prompt before typing is what makes scripted
// keystrokes land on the step that is actually asking.
func (p *PTY) Expect(substr string) *PTY {
	p.t.Helper()
	deadline := time.Now().Add(ptyExpectTimeout)
	for {
		if strings.Contains(p.Screen(), substr) {
			return p
		}
		if time.Now().After(deadline) {
			p.close()
			p.t.Fatalf("the terminal never showed %q within %v; it showed:\n%s",
				substr, ptyExpectTimeout, p.Screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Send types keys on the terminal, exactly as a person would: "\r" is Enter and
// the keys a Wizard lists (space, arrows) are literal.
func (p *PTY) Send(keys string) *PTY {
	p.t.Helper()
	if _, err := p.master.WriteString(keys); err != nil {
		p.t.Fatalf("type %q: %v", keys, err)
	}
	return p
}

// Raw is everything the terminal produced.
func (p *PTY) Raw() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.raw)
}

// Screen renders what the terminal has shown so far as the text a person would
// have read: escape sequences and control characters gone, and a redrawn line
// collapsed to its last rendering.
func (p *PTY) Screen() string { return screenText(p.Raw()) }

// Wait blocks until the process exits and returns the observed result, with
// Stdout carrying the raw terminal stream and Screen the rendered text.
func (p *PTY) Wait() Result {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(ptyExitTimeout):
		p.close()
		p.t.Fatalf("the interactive session did not exit within %v; the terminal showed:\n%s",
			ptyExitTimeout, p.Screen())
	}
	p.res.Stdout = p.Raw()
	p.res.Screen = p.Screen()
	return p.res
}

// pump copies the terminal stream into the session buffer until the terminal
// closes, which happens when the process exits.
func (p *PTY) pump(read chan struct{}) {
	defer close(read)
	buf := make([]byte, 4096)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.raw = append(p.raw, buf[:n]...)
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the process, lets the reader drain the last output, and closes
// the session.
func (p *PTY) reap(read chan struct{}) {
	err := p.cmd.Wait()
	select {
	case <-read:
	case <-time.After(ptyDrainDelay):
	}
	p.master.Close()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		p.res.Exit = 0
	case isExitError(err, &exitErr):
		p.res.Exit = exitErr.ExitCode()
	default:
		p.res.Err = err
	}
	close(p.done)
}

func isExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

// close releases the session. It is safe to call more than once.
func (p *PTY) close() {
	if p.master != nil {
		p.master.Close()
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// screenText renders a terminal stream as the text a person saw. Escape
// sequences and control characters are dropped, a carriage return rewrites the
// line it is on (which is how an interactive redraw collapses to its last
// rendering), and a backspace removes the character before it.
func screenText(raw string) string {
	var out strings.Builder
	line := make([]rune, 0, 128)
	flush := func() {
		out.WriteString(string(line))
		out.WriteByte('\n')
		line = line[:0]
	}
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == 0x1b:
			i = skipEscape(raw, i)
		case c == '\r':
			// A carriage return followed by a newline is just the terminal's
			// line ending; a lone one rewrites the line it is on.
			if i+1 < len(raw) && raw[i+1] == '\n' {
				flush()
				i += 2
				continue
			}
			line = line[:0]
			i++
		case c == '\n':
			flush()
			i++
		case c == '\b':
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			r, size := utf8.DecodeRuneInString(raw[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			line = append(line, r)
			i += size
		}
	}
	if len(line) > 0 {
		out.WriteString(string(line))
	}
	return out.String()
}

// skipEscape returns the index just past the escape sequence starting at i: a
// CSI sequence ends at its final byte, an OSC sequence at BEL or ST, and every
// other introducer takes a single byte with it.
func skipEscape(raw string, i int) int {
	i++ // the ESC itself
	if i >= len(raw) {
		return i
	}
	switch raw[i] {
	case '[':
		i++
		for i < len(raw) {
			if raw[i] >= 0x40 && raw[i] <= 0x7e {
				return i + 1
			}
			i++
		}
		return i
	case ']', 'P', '^', '_':
		i++
		for i < len(raw) {
			if raw[i] == 0x07 {
				return i + 1
			}
			if raw[i] == 0x1b && i+1 < len(raw) && raw[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}
