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
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/instance"
)

// GatewayServiceName is the compose service the Gateway runs as. One Instance is
// one Gateway, so every mode-specific verb names it.
const GatewayServiceName = "gateway"

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
}

// Service is one compose service as `compose ps` reports it.
type Service struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

// Up builds if needed and starts the Gateway detached.
func (c Compose) Up() *contract.Fault {
	stdout, stderr, err := c.exec("up", "--detach", "--build", GatewayServiceName)
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
	stdout, stderr, err := c.exec("ps", "--format", "json")
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
	stdout, stderr, err := c.exec(verb...)
	if err != nil {
		return stdout + stderr, c.faultOf("logs", stdout, stderr, err)
	}
	return stdout, nil
}

// args renders the frozen prefix every compose call carries.
func (c Compose) args(verb ...string) []string {
	out := []string{"compose", "--project-name", c.Namespace, "--file", c.File, "--env-file", c.EnvFile}
	return append(out, verb...)
}

func (c Compose) exec(verb ...string) (stdout, stderr string, err error) {
	cmd := exec.Command("docker", c.args(verb...)...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
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
	return contract.NewFault(contract.CodeDocker, contract.ExitFailure,
		fmt.Sprintf("%s failed: %s", action, detail)).
		WithCause(err).WithRemediation(doctorRemediation())
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
