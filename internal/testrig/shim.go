package testrig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ShimDockerFile is the docker stand-in's filename inside the shim directory.
const ShimDockerFile = "docker"

// dockerShimScript is a POSIX sh stand-in for the docker CLI. It records every
// invocation as one JSON object per line, so a test can assert exactly what the
// CLI asked for, and it answers with canned stdout / a chosen exit code when the
// environment names them — the knobs the gateway and setup tickets need.
//
// One JSON object per line; an argument containing a raw newline is not
// representable, which no test does.
const dockerShimScript = `#!/bin/sh
# igdev test rig docker shim: records invocations, replays canned answers.
state="$IGDEV_SHIM_DOCKER_STATE"
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
if [ -n "$state" ]; then
  mkdir -p "$(dirname "$state")"
  n=1
  if [ -f "$state" ]; then n=$(( $(wc -l < "$state") + 1 )); fi
  argv=""
  for arg in "$@"; do
    if [ -n "$argv" ]; then argv="$argv,"; fi
    argv="$argv\"$(escape "$arg")\""
  done
  printf '{"n":%s,"cwd":"%s","argv":[%s]}\n' "$n" "$(escape "$PWD")" "$argv" >> "$state"
fi
if [ -n "$IGDEV_SHIM_DOCKER_OUT" ]; then printf '%s\n' "$IGDEV_SHIM_DOCKER_OUT"; fi
exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
`

// ShimDocker installs the fake docker on the scratch PATH and returns the state
// file path (the docker call log). Calling it twice is harmless.
func (e *Env) ShimDocker() string {
	e.T.Helper()
	state := e.Path("state", "docker-calls.jsonl")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		e.T.Fatalf("mkdir shim state: %v", err)
	}
	path := filepath.Join(e.Shim, ShimDockerFile)
	if err := os.WriteFile(path, []byte(dockerShimScript), 0o755); err != nil {
		e.T.Fatalf("write docker shim: %v", err)
	}
	e.SetBaseEnv("IGDEV_SHIM_DOCKER_STATE=" + state)
	e.RegisterReplacement(state, "<DOCKER_STATE>")
	return state
}

// DockerCall is one recorded docker invocation.
type DockerCall struct {
	N    int      `json:"n"`
	CWD  string   `json:"cwd"`
	Argv []string `json:"argv"`
}

// DockerCalls returns the shim's call log, oldest first. An absent log means no
// docker was ever invoked.
func (e *Env) DockerCalls(t *testing.T) []DockerCall {
	t.Helper()
	raw, err := os.ReadFile(e.Path("state", "docker-calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read docker shim state: %v", err)
	}
	var calls []DockerCall
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call DockerCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("docker shim state line %q: %v", line, err)
		}
		calls = append(calls, call)
	}
	return calls
}

// Orphans counts docker objects the shim saw created and never removed: the
// hygiene gate behind "zero orphan docker objects per fixture run".
type Orphans struct {
	Containers int
	Networks   int
	Volumes    int
}

// Total is the number of leaked objects.
func (o Orphans) Total() int { return o.Containers + o.Networks + o.Volumes }

// String names the kinds that leaked, for failure messages.
func (o Orphans) String() string {
	parts := []string{}
	if o.Containers > 0 {
		parts = append(parts, "containers")
	}
	if o.Networks > 0 {
		parts = append(parts, "networks")
	}
	if o.Volumes > 0 {
		parts = append(parts, "volumes")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// DockerOrphans computes the leak tally from the shim's call log. Recognition is
// literal: a create-family verb adds, an rm-family verb subtracts, and the count
// never drops below zero.
func (e *Env) DockerOrphans(t *testing.T) Orphans {
	t.Helper()
	var o Orphans
	for _, call := range e.DockerCalls(t) {
		argv := dropCommandName(call.Argv)
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "run", "create":
			o.Containers++
		case "rm":
			o.Containers = sub(o.Containers, positional(argv[1:]))
		case "network", "volume":
			if argv[1] == "create" {
				o = bumpObject(o, argv[0])
			} else if argv[1] == "rm" || argv[1] == "remove" {
				o = removeObject(o, argv[0], positional(argv[2:]))
			}
		}
	}
	return o
}

// AssertNoDockerOrphans fails when the run created docker objects it did not
// clean up.
func (e *Env) AssertNoDockerOrphans(t *testing.T) {
	t.Helper()
	if got := e.DockerOrphans(t); got.Total() > 0 {
		t.Errorf("orphan docker objects: %d (%s)", got.Total(), got)
	}
}

// AssertNoDockerCalls fails when the run touched docker at all: startup and
// status must stay off the engine.
func (e *Env) AssertNoDockerCalls(t *testing.T) {
	t.Helper()
	if calls := e.DockerCalls(t); len(calls) > 0 {
		t.Errorf("expected no docker invocations, got %d: %+v", len(calls), calls)
	}
}

func bumpObject(o Orphans, kind string) Orphans {
	if kind == "network" {
		o.Networks++
	} else {
		o.Volumes++
	}
	return o
}

func removeObject(o Orphans, kind string, removed int) Orphans {
	if kind == "network" {
		o.Networks = sub(o.Networks, removed)
	} else {
		o.Volumes = sub(o.Volumes, removed)
	}
	return o
}

func dropCommandName(argv []string) []string {
	if len(argv) > 0 && filepath.Base(argv[0]) == ShimDockerFile {
		return argv[1:]
	}
	return argv
}

// positional counts non-flag arguments, ignoring the value of `-f`.
func positional(argv []string) int {
	count := 0
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		count++
	}
	return count
}

func sub(current, removed int) int {
	if current-removed < 0 {
		return 0
	}
	return current - removed
}
