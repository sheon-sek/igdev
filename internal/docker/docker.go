// Package docker drives the container engine the Gateway lifecycle needs.
//
// Every call goes through `docker compose` with the Instance's project name, the
// Compose file `igdev setup` rendered, and the matching environment file, so the
// project a command touches is never ambiguous and a parallel worktree's Instance
// cannot be addressed by mistake. Nothing in this package knows about ports or
// URLs: a URL is always the recorded one, and compose is only ever asked about
// the project it was given.
package docker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/instance"
)

// GatewayServiceName is the compose service the Gateway runs as. One Instance is
// one Gateway, so every mode-specific verb names it.
const GatewayServiceName = "gateway"

// GatewayContainer is the name Compose gives the Gateway container of the
// Instance whose project is namespace: one replica, so always index 1.
func GatewayContainer(namespace string) string {
	return namespace + "-" + GatewayServiceName + "-1"
}

// TrialKeeperServiceName is the service that resets the Gateway's expired trial in
// place, when the contract asks for it (ADR 0008).
const TrialKeeperServiceName = "trial-keeper"

// Compose addresses one Instance's compose project.
type Compose struct {
	// Namespace is the project name, igdev-<short instance id>.
	Namespace string
	// File is the rendered Compose file and EnvFile its environment file.
	File    string
	EnvFile string
	// Dir is the working directory compose runs in: the Project Root.
	Dir string
	// Env carries the environment entries the Compose file interpolates: the
	// Gateway admin credentials, and the staged Baseline's restore arguments
	// (empty when nothing is staged). They travel here, not in a rendered file,
	// which is what keeps compose.env safe to diff and golden and keeps the
	// rendered Compose file identical whether or not a Baseline is staged.
	Env []string
	// Progress, when set, receives the engine's own output as it arrives. A
	// lifecycle call is minutes long — `up --detach --build` builds or pulls the
	// image before it creates anything — so a verb that can be slow streams its
	// output here rather than leaving the caller with a blank terminal until the
	// call returns. Verbs whose output igdev parses (`ps`, `logs`) never use it.
	Progress io.Writer
	// TrialKeeper is whether the rendered project runs the trial keeper, which
	// `up` then starts next to the Gateway.
	TrialKeeper bool
}

// Service is one compose service as `compose ps` reports it.
type Service struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

// UpServices are the services `up` starts: the Gateway, and the trial keeper
// when the project runs one.
func (c Compose) UpServices() []string {
	if c.TrialKeeper {
		return []string{GatewayServiceName, TrialKeeperServiceName}
	}
	return []string{GatewayServiceName}
}

// Up builds if needed and starts the Gateway detached.
func (c Compose) Up() *contract.Fault {
	stdout, stderr, err := c.exec(append([]string{"up", "--detach", "--build"}, c.UpServices()...)...)
	return c.faultOf("up", stdout, stderr, err)
}

// Down stops and removes the project's containers. volumes also removes the named
// volume, which is what discards the Gateway's data.
func (c Compose) Down(volumes bool) *contract.Fault {
	verb := []string{"down", "--remove-orphans"}
	if volumes {
		verb = []string{"down", "--volumes", "--remove-orphans"}
	}
	stdout, stderr, err := c.exec(verb...)
	return c.faultOf("down", stdout, stderr, err)
}

// Restart restarts the Gateway container in place, keeping its volume.
func (c Compose) Restart() *contract.Fault {
	stdout, stderr, err := c.exec("restart", GatewayServiceName)
	return c.faultOf("restart", stdout, stderr, err)
}

// Ps reports the project's containers.
func (c Compose) Ps() ([]Service, *contract.Fault) {
	stdout, stderr, err := c.capture("ps", "--format", "json")
	if err != nil {
		return nil, c.faultOf("ps", stdout, stderr, err)
	}
	services, decodeErr := decodeServices(stdout)
	if decodeErr != nil {
		return nil, contract.NewFault(contract.CodeDocker, contract.ExitFailure,
			fmt.Sprintf("docker compose ps reported output igdev cannot read: %v", decodeErr)).
			WithCause(decodeErr).
			WithRemediation(doctorRemediation())
	}
	return services, nil
}

// Logs returns the Gateway's container log. tail limits it to the last N lines;
// zero means everything the engine still holds.
func (c Compose) Logs(tail int) (string, *contract.Fault) {
	verb := []string{"logs"}
	if tail > 0 {
		verb = append(verb, "--tail", fmt.Sprint(tail))
	}
	verb = append(verb, GatewayServiceName)
	stdout, stderr, err := c.capture(verb...)
	if err != nil {
		return stdout + stderr, c.faultOf("logs", stdout, stderr, err)
	}
	return stdout, nil
}

// ExecRequest is one command run inside the Gateway container.
type ExecRequest struct {
	// User is the container user, `ignition`, `root`, or a uid:gid.
	User string
	// Workdir, when set, is the working directory inside the container.
	Workdir string
	// Argv is the command and its arguments.
	Argv []string
	// Stdin, Stdout, and Stderr are the command's streams; a nil Stdin sends none.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Exec runs one command in the Gateway container without a TTY and reports its
// exit code. A non-nil error means the command could not be run at all (no
// docker); a command that ran and failed is exit != 0 with a nil error, because
// `docker compose exec` passes the command's own exit code through.
func (c Compose) Exec(req ExecRequest) (int, error) {
	verb := []string{"exec", "-T"}
	if req.User != "" {
		verb = append(verb, "--user", req.User)
	}
	if req.Workdir != "" {
		verb = append(verb, "--workdir", req.Workdir)
	}
	verb = append(verb, GatewayServiceName)
	verb = append(verb, req.Argv...)
	cmd := exec.Command("docker", c.args(verb...)...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin = req.Stdin
	cmd.Stdout, cmd.Stderr = req.Stdout, req.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// args renders the frozen prefix every compose call carries.
func (c Compose) args(verb ...string) []string {
	out := []string{"compose", "--project-name", c.Namespace, "--file", c.File, "--env-file", c.EnvFile}
	return append(out, verb...)
}

// exec runs compose, streaming the engine's output to Progress while also
// capturing it: a long build or pull reports progress, and the last line is still
// there to report when the call fails.
func (c Compose) exec(verb ...string) (stdout, stderr string, err error) {
	return c.run(c.progress(), verb...)
}

// capture runs compose without streaming: the output is igdev's to parse, not the
// caller's to watch.
func (c Compose) capture(verb ...string) (stdout, stderr string, err error) {
	return c.run(nil, verb...)
}

func (c Compose) run(progress io.Writer, verb ...string) (stdout, stderr string, err error) {
	cmd := exec.Command("docker", c.args(verb...)...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = sink(&out, progress), sink(&errBuf, progress)
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// progress wraps Progress in a lock: exec copies the two output streams with one
// goroutine each, and both land on that one destination.
func (c Compose) progress() io.Writer {
	if c.Progress == nil {
		return nil
	}
	return &lockWriter{w: c.Progress}
}

// sink tee's one child stream into its buffer and, when streaming, into progress.
func sink(buf *bytes.Buffer, progress io.Writer) io.Writer {
	if progress == nil {
		return buf
	}
	return io.MultiWriter(buf, progress)
}

// lockWriter serializes writes from compose's two output copiers onto Progress,
// so Progress can be any writer, including one that is not safe for concurrent
// use on its own.
type lockWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (c Compose) faultOf(action, stdout, stderr string, err error) *contract.Fault {
	if err == nil {
		return nil
	}
	return Fault("docker compose "+action, stdout+stderr, err)
}

// Fault maps a failed docker invocation onto the contract. The reason is the
// engine's own last line, so the message says what the engine objected to rather
// than only that a command failed.
func Fault(action, output string, err error) *contract.Fault {
	if notFound(err) {
		return contract.NewFault(contract.CodeDocker, contract.ExitFailure,
			fmt.Sprintf("docker is not available: %v", err)).
			WithCause(err).WithRemediation(doctorRemediation())
	}
	detail := lastLine(output)
	if detail == "" {
		detail = err.Error()
	}
	if DaemonDown(output) {
		return contract.NewFault(contract.CodeDockerDaemon, contract.ExitFailure,
			fmt.Sprintf("%s failed: the Docker daemon is not reachable: %s", action, detail)).
			WithCause(err).
			WithRemediation(contract.Remediation{
				Command: "igdev doctor",
				Why:     "start Docker first; this audits that the engine answers again",
			})
	}
	return contract.NewFault(contract.CodeDocker, contract.ExitFailure,
		fmt.Sprintf("%s failed: %s", action, detail)).
		WithCause(err).WithRemediation(doctorRemediation())
}

// daemonDown matches what the docker CLI prints when it has no engine to talk
// to: a stopped daemon, a missing socket, or a context pointing nowhere.
var daemonDown = []string{
	"Cannot connect to the Docker daemon",
	"failed to connect to the docker API",
	"if the daemon is running",
	"Is the docker daemon running?",
	"error during connect",
	"docker.sock: connect: no such file or directory",
	"docker.sock: connect: connection refused",
}

// DaemonDown reports whether engine output says the daemon is unreachable, as
// opposed to a compose call the engine received and refused.
func DaemonDown(output string) bool {
	for _, marker := range daemonDown {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

// Running names the igdev compose projects this machine is running, so the
// Capacity Gate can say what is holding the memory instead of only refusing.
// Listing is best effort: an engine that cannot be asked lists nothing, and a
// capacity refusal must never depend on a second docker call succeeding.
func Running() []string {
	cmd := exec.Command("docker", "compose", "ls", "--format", "json")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return nil
	}
	var projects []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &projects); err != nil {
		return nil
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		if strings.HasPrefix(project.Name, instance.NamespacePrefix) {
			names = append(names, project.Name)
		}
	}
	return names
}

// decodeServices reads `compose ps --format json`, accepting both shapes the
// engine has used: one JSON array, and one JSON object per line.
func decodeServices(raw string) ([]Service, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var list []Service
	if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
		return list, nil
	}
	var services []Service
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var one Service
		if err := json.Unmarshal([]byte(line), &one); err != nil {
			return nil, err
		}
		services = append(services, one)
	}
	return services, nil
}

// lastLine is the last non-empty line of engine output: the line that carries the
// objection.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func notFound(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, fs.ErrNotExist)
	}
	return false
}

func doctorRemediation() contract.Remediation {
	return contract.Remediation{
		Command: "igdev doctor",
		Why:     "audit the host prerequisites igdev's runtime work depends on",
	}
}
