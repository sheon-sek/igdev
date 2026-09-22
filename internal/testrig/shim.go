package testrig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ShimDockerFile is the docker stand-in's filename inside the shim directory.
const ShimDockerFile = "docker"

// dockerShimScript is a POSIX sh stand-in for the docker CLI. It records every
// invocation as one JSON object per line, so a test can assert exactly what the
// CLI asked for, and it answers with canned stdout, or with a deterministic
// synthetic container id for run/create, or with a chosen exit code — the knobs
// the setup and gateway tickets drive.
//
// Recognition of what was created or destroyed happens in Go (DockerOrphans),
// keeping this script dumb. One JSON object per line; an argument containing a
// raw newline is not representable, which no test does.
const dockerShimScript = `#!/bin/sh
# igdev test rig docker shim: records invocations, replays canned answers.
state="$IGDEV_SHIM_DOCKER_STATE"
escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
n=1
if [ -n "$state" ] && [ -f "$state" ]; then
  mkdir -p "$(dirname "$state")"
  n=$(( $(wc -l < "$state") + 1 ))
fi
if [ -n "$state" ]; then
  mkdir -p "$(dirname "$state")"
  argv=""
  for arg in "$@"; do
    if [ -n "$argv" ]; then argv="$argv,"; fi
    argv="$argv\"$(escape "$arg")\""
  done
  printf '{"n":%s,"cwd":"%s","argv":[%s]}\n' "$n" "$(escape "$PWD")" "$argv" >> "$state"
fi
if [ -n "$IGDEV_SHIM_DOCKER_OUT" ]; then
  printf '%s\n' "$IGDEV_SHIM_DOCKER_OUT"
elif [ "$1" = "run" ] || [ "$1" = "create" ]; then
  printf '%064x\n' "$n"
fi
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

// SyntheticContainerID is the id the shim echoes for the nth run/create call, and
// the identity an unnamed container is tracked under.
func SyntheticContainerID(n int) string { return fmt.Sprintf("%064x", n) }

// objectRef is one docker object the shim saw created.
type objectRef struct {
	kind string // container, network, volume
	id   string
}

// Orphans are the docker objects the shim saw created and never removed, tracked
// by identity rather than by count: creating one thing and removing another is
// still one orphan. Empty lists mean a clean run.
type Orphans struct {
	Containers []string
	Networks   []string
	Volumes    []string
	// Unmatched are removals that named nothing this run created, reported so a
	// test can tell a typo from a real leak.
	Unmatched []string
}

// Total is the number of leaked objects.
func (o Orphans) Total() int { return len(o.Containers) + len(o.Networks) + len(o.Volumes) }

// String names the leaked objects, for failure messages.
func (o Orphans) String() string {
	if o.Total() == 0 && len(o.Unmatched) == 0 {
		return "none"
	}
	groups := []struct {
		label string
		names []string
	}{
		{"containers", o.Containers},
		{"networks", o.Networks},
		{"volumes", o.Volumes},
		{"unmatched removals", o.Unmatched},
	}
	parts := []string{}
	for _, group := range groups {
		if len(group.names) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", group.label, strings.Join(group.names, ",")))
		}
	}
	return strings.Join(parts, "; ")
}

// DockerOrphans replays the call log: a creation registers an identity, a removal
// retires that identity. `docker run --rm` is never registered because the engine
// removes it at exit. A removal naming something unknown is reported as unmatched
// and retires nothing, so mismatched cleanups cannot hide a leak.
//
// Compose verbs (`docker compose up|down`) arrive with the gateway ticket, which
// owns the project namespace; until then they are recorded but not tallied.
func (e *Env) DockerOrphans(t *testing.T) Orphans {
	t.Helper()
	alive := map[string]objectRef{}
	var unmatched []string

	retire := func(kind, name string, n int) {
		key := kind + ":" + name
		if _, ok := alive[key]; ok {
			delete(alive, key)
			return
		}
		matches := []string{}
		for key, ref := range alive {
			if ref.kind == kind && strings.HasPrefix(ref.id, name) {
				matches = append(matches, key)
			}
		}
		if len(matches) == 1 {
			delete(alive, matches[0])
			return
		}
		unmatched = append(unmatched, fmt.Sprintf("%s@call%d", name, n))
	}

	for _, call := range e.DockerCalls(t) {
		argv := dropCommandName(call.Argv)
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "run", "create":
			if hasFlag(argv, "--rm") {
				continue
			}
			id := flagValue(argv, "--name")
			if id == "" {
				id = SyntheticContainerID(call.N)
			}
			alive["container:"+id] = objectRef{kind: "container", id: id}
		case "rm", "rmi":
			for _, name := range positional(argv[1:]) {
				retire("container", name, call.N)
			}
		case "network", "volume":
			if len(argv) < 2 {
				continue
			}
			switch argv[1] {
			case "create":
				name := firstPositional(argv[2:])
				if name == "" {
					name = SyntheticContainerID(call.N)
				}
				alive[argv[0]+":"+name] = objectRef{kind: argv[0], id: name}
			case "rm", "remove":
				for _, name := range positional(argv[2:]) {
					retire(argv[0], name, call.N)
				}
			}
		}
	}

	o := Orphans{Unmatched: unmatched}
	for key, ref := range alive {
		name := strings.TrimPrefix(key, ref.kind+":")
		switch ref.kind {
		case "container":
			o.Containers = append(o.Containers, name)
		case "network":
			o.Networks = append(o.Networks, name)
		case "volume":
			o.Volumes = append(o.Volumes, name)
		}
	}
	sort.Strings(o.Containers)
	sort.Strings(o.Networks)
	sort.Strings(o.Volumes)
	sort.Strings(o.Unmatched)
	return o
}

// AssertNoDockerOrphans fails when the run created docker objects it did not
// clean up.
func (e *Env) AssertNoDockerOrphans(t *testing.T) {
	t.Helper()
	got := e.DockerOrphans(t)
	if got.Total() > 0 {
		t.Errorf("orphan docker objects: %s", got)
	}
	if len(got.Unmatched) > 0 {
		t.Errorf("docker cleanup named objects that were never created: %s", got)
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

func dropCommandName(argv []string) []string {
	if len(argv) > 0 && filepath.Base(argv[0]) == ShimDockerFile {
		return argv[1:]
	}
	return argv
}

// positional returns the non-flag arguments.
func positional(argv []string) []string {
	out := []string{}
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// firstPositional returns the first non-flag argument, or "".
func firstPositional(argv []string) string {
	names := positional(argv)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// hasFlag reports a boolean flag, in either --flag or --flag=value form.
func hasFlag(argv []string, flag string) bool {
	for _, arg := range argv {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

// flagValue returns the value of --flag value or --flag=value, or "".
func flagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value
		}
	}
	return ""
}
