package docker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker puts a `docker` on PATH that prints far more than any diagnostic
// budget: FAKE_OUT bytes of numbered lines on stdout, a burst on stderr, then a
// final stderr line, and exits FAKE_EXIT.
func fakeDocker(t *testing.T, exit string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
seq 1 400000 | sed 's/^/out line /'
seq 1 200000 | sed 's/^/err line /' >&2
printf 'final: engine objected' >&2
exit $FAKE_EXIT
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_EXIT", exit)
}

func testCompose() Compose {
	return Compose{Namespace: "igdev-test", File: "c.yaml", EnvFile: "c.env"}
}

// Human-mode logs stream byte-identically to what the capturing path returns.
func TestStreamLogsMatchesCapturedLogs(t *testing.T) {
	fakeDocker(t, "0")
	c := testCompose()
	want, fault := c.Logs(0)
	if fault != nil {
		t.Fatal(fault)
	}
	var got bytes.Buffer
	if fault := c.StreamLogs(0, &got); fault != nil {
		t.Fatal(fault)
	}
	if got.String() != want || len(want) < 4<<20 {
		t.Fatalf("streamed %d bytes, captured %d", got.Len(), len(want))
	}
}

func TestStreamLogsFailureReportsLastLine(t *testing.T) {
	fakeDocker(t, "1")
	if fault := testCompose().StreamLogs(0, &bytes.Buffer{}); fault == nil ||
		!strings.Contains(fault.Error(), "final: engine objected") {
		t.Fatalf("fault = %v", fault)
	}
}

// Lifecycle runs retain only a bounded tail, with progress on and off, on
// success and failure, and a failure still names the engine's last line, even
// when it is unterminated.
func TestExecRetainsBoundedTail(t *testing.T) {
	for _, exit := range []string{"0", "1"} {
		for _, streaming := range []bool{false, true} {
			fakeDocker(t, exit)
			c := testCompose()
			var progress bytes.Buffer
			if streaming {
				c.Progress = &progress
			}
			stdout, stderr, err := c.exec("up")
			if (err != nil) != (exit == "1") {
				t.Fatalf("exit %s: err = %v", exit, err)
			}
			if len(stdout) > diagnosticTailBytes || len(stderr) > diagnosticTailBytes {
				t.Fatalf("retained %d+%d bytes", len(stdout), len(stderr))
			}
			if !strings.HasSuffix(stdout, "out line 400000\n") || !strings.HasSuffix(stderr, "final: engine objected\n") {
				t.Fatalf("tail lost the last lines: %q / %q", stdout[len(stdout)-40:], stderr[len(stderr)-40:])
			}
			if streaming && progress.Len() < 8<<20 {
				t.Fatalf("progress got %d bytes, want all output", progress.Len())
			}
			if exit == "1" {
				if fault := c.faultOf("up", stdout, stderr, err); !strings.Contains(fault.Error(), "final: engine objected") {
					t.Fatalf("fault = %v", fault)
				}
			}
		}
	}
}
