package testrig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/capacity"
)

// ShimDockerFile is the docker stand-in's filename inside the shim directory.
const ShimDockerFile = "docker"

// dockerShimScript is a POSIX sh stand-in for the docker CLI. It records every
// invocation as one JSON object per line, so a test can assert exactly what the
// CLI asked for, and it answers with canned stdout, or with a deterministic
// synthetic container id for run/create, or with a chosen exit code — the knobs
// the setup and gateway tickets drive.
//
// The compose idioms an Instance's lifecycle needs are answered from the state
// the shim keeps beside the call log: `up` records the project's container and
// named volume (reading the published ports out of the rendered Compose file, so
// they are the recorded triplet), `ps` reports them as a running service, `logs`
// replays a canned line, `ls` lists the projects a machine is running, and `down
// --volumes` retires both objects. Everything else the script ignores.
//
// Recognition of what was created or destroyed happens in Go (DockerOrphans),
// keeping this script dumb. One JSON object per line; an argument containing a
// raw newline is not representable, which no test does.
const dockerShimScript = `#!/bin/sh
# igdev test rig docker shim: records invocations, replays canned answers, and
# keeps the container and volume state a compose project owns, beside the call
# log. Object state lives under "<state dir>/docker/", so one scratch machine
# sees one engine.
state="$IGDEV_SHIM_DOCKER_STATE"
home=""
if [ -n "$state" ]; then
  home="$(dirname "$state")/docker"
fi
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
  # The GATEWAY_* variables igdev hands the engine: the rendered Compose file
  # interpolates them, so the call log has to carry them for a test to assert the
  # wiring — a service command has no compose CLI flag, and a credential or a
  # restore argument is only observable where the engine receives it.
  vars=""
  for name in GATEWAY_ADMIN_USERNAME GATEWAY_ADMIN_PASSWORD GATEWAY_RESTORE_ARGS; do
    eval "value=\${$name}"
    if [ -n "$vars" ]; then vars="$vars,"; fi
    vars="$vars\"$name\":\"$(escape "$value")\""
  done
  printf '{"n":%s,"cwd":"%s","argv":[%s],"env":{%s}}\n' "$n" "$(escape "$PWD")" "$argv" "$vars" >> "$state"
fi
if [ -n "$IGDEV_SHIM_DOCKER_OUT" ]; then
  printf '%s\n' "$IGDEV_SHIM_DOCKER_OUT"
  exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
fi
[ $# -gt 0 ] || exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
if [ "$1" = "run" ] || [ "$1" = "create" ]; then
  printf '%064x\n' "$n"
  exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
fi
if [ "$1" != "compose" ] || [ -z "$home" ]; then
  exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
fi
shift
project=""; file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p|--project-name) project="$2"; shift 2 ;;
    --project-name=*) project="${1#*=}"; shift ;;
    -f|--file) file="$2"; shift 2 ;;
    --file=*) file="${1#*=}"; shift ;;
    --env-file|--project-directory|--profile|--ansi|--progress) shift 2 ;;
    --*=*|-*) shift ;;
    *) break ;;
  esac
done
verb="$1"
[ $# -gt 0 ] && shift
# A real compose takes the project name from the file's name: key when no flag
# says otherwise, so the shim does the same.
if [ -z "$project" ] && [ -n "$file" ] && [ -f "$file" ]; then
  project="$(sed -n 's/^name:[[:space:]]*\(.*\)$/\1/p' "$file" | head -n 1)"
fi
container="$project-gateway-1"
volume="${project}_gateway-data"
mkdir -p "$home/containers" "$home/volumes"
case "$verb" in
  up)
    ports="$(sed -n 's/.*127\.0\.0\.1:\([0-9][0-9]*\):[0-9][0-9]*.*/\1/p' "$file" 2>/dev/null | tr '\n' ' ')"
    printf 'project=%s\nports=%s\n' "$project" "$ports" > "$home/containers/$container"
    printf 'project=%s\n' "$project" > "$home/volumes/$volume"
    printf ' Container %s  Started\n' "$container"
    ;;
  down)
    volumes=no
    for a in "$@"; do [ "$a" = "--volumes" ] && volumes=yes; done
    rm -f "$home/containers/$container"
    [ "$volumes" = yes ] && rm -f "$home/volumes/$volume"
    printf ' Container %s  Removed\n' "$container"
    ;;
  restart)
    printf ' Container %s  Restarted\n' "$container"
    ;;
  ps)
    printf '['
    sep=""
    for f in "$home"/containers/*; do
      [ -f "$f" ] || continue
      [ "$(sed -n 's/^project=//p' "$f")" = "$project" ] || continue
      ports="$(sed -n 's/^ports=//p' "$f")"
      http="$(printf '%s' "$ports" | cut -d' ' -f1)"
      https="$(printf '%s' "$ports" | cut -d' ' -f2)"
      dbg="$(printf '%s' "$ports" | cut -d' ' -f3)"
      printf '%s{"Name":"%s","Service":"gateway","State":"running","Health":"","ExitCode":0,"Status":"Up 1 second","Publishers":[{"URL":"127.0.0.1","TargetPort":8088,"PublishedPort":%s,"Protocol":"tcp"},{"URL":"127.0.0.1","TargetPort":8043,"PublishedPort":%s,"Protocol":"tcp"},{"URL":"127.0.0.1","TargetPort":8000,"PublishedPort":%s,"Protocol":"tcp"}]}' "$sep" "$(basename "$f")" "${http:-0}" "${https:-0}" "${dbg:-0}"
      sep=","
    done
    printf ']\n'
    ;;
  logs)
    printf '%s  | igdev shim: gateway log line 1\n' "$container"
    printf '%s  | igdev shim: gateway log line 2\n' "$container"
    ;;
  ls)
    printf '['
    sep=""
    for f in "$home"/containers/*; do
      [ -f "$f" ] || continue
      printf '%s{"Name":"%s","Status":"running(1)","ConfigFiles":"-"}' "$sep" "$(sed -n 's/^project=//p' "$f")"
      sep=","
    done
    printf ']\n'
    ;;
esac
exit "${IGDEV_SHIM_DOCKER_EXIT:-0}"
`

// ShimJavaFile is the java stand-in's filename inside the shim directory.
const ShimJavaFile = "java"

// javaShimScript is a POSIX sh stand-in for the JVM igdev launches for the
// batched Jython check. It records every invocation as one JSON object per line
// — the code the CLI passed with -c collapses to <DRIVER>, because a multi-line
// argument is not representable in this format — and answers instantly.
//
// Unless IGDEV_SHIM_JAVA_FAST=1, it also emulates the JVM faithfully enough to
// produce real diagnostics: the driver the CLI passed with -c is executed by
// python3 (present on every supported host) against the same file arguments. A
// syntax error therefore reaches igdev as the same `path:line: message` line a
// real JVM would print. IGDEV_SHIM_JAVA_EXIT/IGDEV_SHIM_JAVA_OUT replace all of
// that with a canned answer, for a failure the driver cannot produce.
const javaShimScript = `#!/bin/sh
state="$IGDEV_SHIM_JAVA_STATE"
if [ -n "$state" ]; then
  mkdir -p "$(dirname "$state")"
  n=1
  [ -f "$state" ] && n=$(( $(wc -l < "$state") + 1 ))
  escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
  argv=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "-c" ]; then arg="<DRIVER>"; fi
    if [ -n "$argv" ]; then argv="$argv,"; fi
    argv="$argv\"$(escape "$arg")\""
    previous="$arg"
  done
  printf '{"n":%s,"cwd":"%s","argv":[%s]}\n' "$n" "$(escape "$PWD")" "$argv" >> "$state"
fi
if [ -n "$IGDEV_SHIM_JAVA_EXIT" ]; then
  [ -n "$IGDEV_SHIM_JAVA_OUT" ] && printf '%s\n' "$IGDEV_SHIM_JAVA_OUT"
  exit "$IGDEV_SHIM_JAVA_EXIT"
fi
if [ "$IGDEV_SHIM_JAVA_FAST" = "1" ]; then exit 0; fi
code=""
while [ $# -gt 0 ]; do
  case "$1" in
    -jar) shift 2 ;;
    -c) code="$2"; shift 2 ;;
    *) break ;;
  esac
done
if [ -n "$code" ] && command -v python3 >/dev/null 2>&1; then
  exec python3 -c "$code" "$@"
fi
exit 0
`

// ShimJava installs the fake JVM on the scratch PATH and returns the state file
// path (the invocation log). Calling it twice is harmless.
func (e *Env) ShimJava() string {
	e.T.Helper()
	state := e.Path("state", "java-calls.jsonl")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		e.T.Fatalf("mkdir shim state: %v", err)
	}
	path := filepath.Join(e.Shim, ShimJavaFile)
	if err := os.WriteFile(path, []byte(javaShimScript), 0o755); err != nil {
		e.T.Fatalf("write java shim: %v", err)
	}
	e.SetBaseEnv("IGDEV_SHIM_JAVA_STATE=" + state)
	e.RegisterReplacement(state, "<JAVA_STATE>")
	return state
}

// JavaCall is one recorded JVM invocation.
type JavaCall struct {
	N    int      `json:"n"`
	CWD  string   `json:"cwd"`
	Argv []string `json:"argv"`
}

// Jar is the jar the JVM was told to run, or "" when it was not a -jar call.
func (c JavaCall) Jar() string {
	for i := 0; i+1 < len(c.Argv); i++ {
		if c.Argv[i] == "-jar" {
			return c.Argv[i+1]
		}
	}
	return ""
}

// Files lists the file arguments the JVM was given: everything after the -c
// driver.
func (c JavaCall) Files() []string {
	for i := range c.Argv {
		if c.Argv[i] != "-c" {
			continue
		}
		if i+2 > len(c.Argv) {
			return nil
		}
		return c.Argv[i+2:]
	}
	return nil
}

// JavaCalls returns the shim's call log, oldest first. An absent log means no JVM
// was ever launched.
func (e *Env) JavaCalls(t *testing.T) []JavaCall {
	t.Helper()
	raw, err := os.ReadFile(e.Path("state", "java-calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read java shim state: %v", err)
	}
	var calls []JavaCall
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call JavaCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("java shim state line %q: %v", line, err)
		}
		calls = append(calls, call)
	}
	return calls
}

// ShimMeminfo writes a meminfo-format file reporting availableMB of MemAvailable
// and points every later run in this Env at it. The Capacity Gate reads the path
// named by capacity.MeminfoEnv instead of /proc/meminfo, which is how a test
// decides how much free memory the machine appears to have.
func (e *Env) ShimMeminfo(availableMB int) string {
	e.T.Helper()
	path := e.Write("state/meminfo", fmt.Sprintf(
		"MemTotal: %d kB\nMemFree: %d kB\nMemAvailable: %d kB\nBuffers: 0 kB\n",
		(availableMB+1024)*1024, availableMB*1024, availableMB*1024))
	e.SetBaseEnv(capacity.MeminfoEnv + "=" + path)
	e.RegisterReplacement(path, "<MEMINFO>")
	return path
}

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

// ShimActFile is the act stand-in's filename inside the shim directory.
const ShimActFile = "act"

// actShimScript is a POSIX sh stand-in for nektos/act, the runner `igdev
// ci-local` shells out to. It records every invocation as one JSON object per
// line — argv, the working directory, and the environment act would read the
// offline decision from — so a test can assert exactly what the CLI asked for,
// prints canned output, and exits with a chosen code.
//
// IGDEV_SHIM_ACT_LINES prints that many numbered stdout lines, IGDEV_SHIM_ACT_OUT
// one canned stdout line, IGDEV_SHIM_ACT_ERR one stderr line, and
// IGDEV_SHIM_ACT_EXIT the exit code act reports (0 by default). Nothing else
// runs: no docker, no network, no workflow.
const actShimScript = `#!/bin/sh
state="$IGDEV_SHIM_ACT_STATE"
if [ -n "$state" ]; then
  mkdir -p "$(dirname "$state")"
  n=1
  [ -f "$state" ] && n=$(( $(wc -l < "$state") + 1 ))
  escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
  argv=""
  for arg in "$@"; do
    if [ -n "$argv" ]; then argv="$argv,"; fi
    argv="$argv\"$(escape "$arg")\""
  done
  # IGDEV_ACT_OFFLINE is the env tier of the offline decision: recording it makes
  # the environment act inherits observable, not just the flags it was handed.
  printf '{"n":%s,"cwd":"%s","exit":%s,"argv":[%s],"env":{"IGDEV_ACT_OFFLINE":"%s"}}\n' \
    "$n" "$(escape "$PWD")" "${IGDEV_SHIM_ACT_EXIT:-0}" "$argv" "$(escape "$IGDEV_ACT_OFFLINE")" >> "$state"
fi
if [ -n "$IGDEV_SHIM_ACT_LINES" ]; then
  i=1
  while [ "$i" -le "$IGDEV_SHIM_ACT_LINES" ]; do
    printf 'act shim line %s\n' "$i"
    i=$((i+1))
  done
fi
[ -n "$IGDEV_SHIM_ACT_OUT" ] && printf '%s\n' "$IGDEV_SHIM_ACT_OUT"
[ -n "$IGDEV_SHIM_ACT_ERR" ] && printf '%s\n' "$IGDEV_SHIM_ACT_ERR" >&2
exit "${IGDEV_SHIM_ACT_EXIT:-0}"
`

// ShimAct installs the fake act on the scratch PATH and returns the state file
// path (the act call log). Calling it twice is harmless.
func (e *Env) ShimAct() string {
	e.T.Helper()
	state := e.Path("state", "act-calls.jsonl")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		e.T.Fatalf("mkdir shim state: %v", err)
	}
	path := filepath.Join(e.Shim, ShimActFile)
	if err := os.WriteFile(path, []byte(actShimScript), 0o755); err != nil {
		e.T.Fatalf("write act shim: %v", err)
	}
	e.SetBaseEnv("IGDEV_SHIM_ACT_STATE=" + state)
	e.RegisterReplacement(state, "<ACT_STATE>")
	return state
}

// ActCall is one recorded act invocation.
type ActCall struct {
	N int `json:"n"`
	// CWD is the directory act was started in: the Project Root.
	CWD string `json:"cwd"`
	// Exit is the exit code the shim was configured to report.
	Exit int `json:"exit"`
	// Argv is the argument vector act received, act itself excluded.
	Argv []string `json:"argv"`
	// Env is the environment variable set act inherited that igdev's own
	// behaviour depends on.
	Env map[string]string `json:"env"`
}

// ActCalls returns the shim's call log, oldest first. An absent log means act was
// never invoked.
func (e *Env) ActCalls(t *testing.T) []ActCall {
	t.Helper()
	raw, err := os.ReadFile(e.Path("state", "act-calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read act shim state: %v", err)
	}
	var calls []ActCall
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call ActCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("act shim state line %q: %v", line, err)
		}
		calls = append(calls, call)
	}
	return calls
}

// DockerCall is one recorded docker invocation. Env is the GATEWAY_* variable
// set igdev handed the engine: the variables the rendered Compose file
// interpolates, which is how a test observes the Baseline restore wiring the
// engine actually received.
type DockerCall struct {
	N    int               `json:"n"`
	CWD  string            `json:"cwd"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

// GatewayEnv returns the value of one GATEWAY_* variable the engine received.
// An empty string means igdev supplied it empty.
func (c DockerCall) GatewayEnv(name string) string { return c.Env[name] }

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
// The compose idioms are tallied under the names a real engine gives them:
// `docker compose up` creates the project's gateway container and its named
// volume, and `down --volumes` retires both (`down` alone leaves the volume,
// which is exactly the state the operator asked for).
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
		case "compose":
			project := composeProject(argv[1:])
			if project == "" {
				continue
			}
			container := ComposeContainer(project)
			volume := ComposeVolume(project)
			switch ComposeVerb(argv[1:]) {
			case "up":
				alive["container:"+container] = objectRef{kind: "container", id: container}
				alive["volume:"+volume] = objectRef{kind: "volume", id: volume}
			case "down":
				retire("container", container, call.N)
				if hasFlag(argv, "--volumes") {
					retire("volume", volume, call.N)
				}
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

// ComposeContainer and ComposeVolume are the names a compose project's objects
// carry, which is what the orphan tally and the docker shim both register.
func ComposeContainer(project string) string { return project + "-gateway-1" }

// ComposeVolume is the named volume the gateway service mounts.
func ComposeVolume(project string) string { return project + "_gateway-data" }

// composeValueFlags are the compose flags whose value is a separate argument, so
// the verb scan does not mistake a path or a project name for one.
var composeValueFlags = []string{
	"-p", "--project-name", "-f", "--file", "--env-file",
	"--project-directory", "--profile", "--ansi", "--progress",
}

// composeProject returns the project a recorded compose call named.
func composeProject(argv []string) string {
	if project := flagValue(argv, "--project-name"); project != "" {
		return project
	}
	return flagValue(argv, "-p")
}

// ComposeVerb returns the first non-flag argument of a recorded compose call:
// the subcommand (up, down, ps, logs, ls, restart).
func ComposeVerb(argv []string) string {
	for i := 0; i < len(argv); {
		arg := argv[i]
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
		if !strings.Contains(arg, "=") && containsString(composeValueFlags, arg) {
			i += 2
			continue
		}
		i++
	}
	return ""
}

func containsString(haystack []string, needle string) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
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
