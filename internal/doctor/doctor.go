// Package doctor audits the host prerequisites igdev's runtime work depends on.
//
// The audit is read-only and never fails: it reports, per prerequisite, whether
// the tool is on PATH and which version line it printed, or the error that says
// why it could not be used. `igdev doctor --json` is the machine surface; the
// exit level stays 0 so an agent always receives the report itself.
package doctor

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// State is one prerequisite's verdict.
type State string

const (
	// StatePresent means the command ran and printed a version line.
	StatePresent State = "present"
	// StateMissing means the command is not on PATH at all.
	StateMissing State = "missing"
	// StateFailed means the command is on PATH but produced no version line, so
	// igdev cannot use it.
	StateFailed State = "failed"
)

// Prerequisite is one probed command.
type Prerequisite struct {
	// Name is the frozen key this prerequisite is reported under.
	Name string
	// Required marks a tool igdev itself cannot work without. A missing
	// optional tool makes the report informative, not unready.
	Required bool
	// Args is the probe command line, argv[0] first.
	Args []string
}

// Default is the frozen probe list, in report order. Docker and its Compose
// plugin run the Gateway, and a JVM runs the Jython compatibility check; Gradle
// is optional because a project only needs it when its contract declares a
// Gradle command, and act is optional because only `igdev ci-local` needs it.
func Default() []Prerequisite {
	return []Prerequisite{
		{Name: "docker", Required: true, Args: []string{"docker", "--version"}},
		{Name: "compose", Required: true, Args: []string{"docker", "compose", "version"}},
		{Name: "java", Required: true, Args: []string{"java", "--version"}},
		{Name: "gradle", Required: false, Args: []string{"gradle", "--version"}},
		{Name: "act", Required: false, Args: []string{"act", "--version"}},
	}
}

// Entry is one audited prerequisite, as reported.
type Entry struct {
	Name string `json:"name"`
	// Command is the exact probe igdev ran, so a missing tool is reproducible.
	Command  string `json:"command"`
	Required bool   `json:"required"`
	State    State  `json:"state"`
	// Version is the tool's own first output line, trimmed. Empty when it
	// printed none.
	Version string `json:"version"`
	// Error explains a missing or failed prerequisite; empty when present.
	Error string `json:"error"`
	// Warning is set when the tool works but is too old for part of what igdev
	// does with it. It never makes the report unready.
	Warning string `json:"warning,omitempty"`
}

// Report is the whole audit.
type Report struct {
	// Ready is false when any required prerequisite is not present.
	Ready         bool    `json:"ready"`
	Prerequisites []Entry `json:"prerequisites"`
}

// Runner runs one probe and returns its combined output.
type Runner func(args []string) (string, error)

// System is the real runner: the probe's combined stdout and stderr.
func System(args []string) (string, error) {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	return string(out), err
}

// Audit probes every prerequisite and reports. A nil list audits Default().
func Audit(run Runner, list ...Prerequisite) Report {
	if list == nil {
		list = Default()
	}
	report := Report{Ready: true, Prerequisites: make([]Entry, 0, len(list))}
	for _, p := range list {
		entry := probe(p, run)
		if p.Required && entry.State != StatePresent {
			report.Ready = false
		}
		report.Prerequisites = append(report.Prerequisites, entry)
	}
	return report
}

func probe(p Prerequisite, run Runner) Entry {
	entry := Entry{
		Name:     p.Name,
		Command:  strings.Join(p.Args, " "),
		Required: p.Required,
		State:    StatePresent,
	}
	out, err := run(p.Args)
	entry.Version = firstLine(out)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		entry.State = StateMissing
		entry.Error = fmt.Sprintf("%s is not on PATH", p.Args[0])
	case entry.Version == "":
		// On PATH but unusable: every entry carries either a version line or the
		// reason it could not be read, never neither.
		entry.State = StateFailed
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Error = "printed no version line"
		}
	}
	if entry.State == StatePresent && p.Name == "docker" {
		entry.Warning = dockerWarning(entry.Version)
	}
	return entry
}

// hostGatewayEngine is the first Docker Engine that maps the special
// `host-gateway` address, which the Gateway reaches the host through.
var hostGatewayEngine = [2]int{20, 10}

// dockerVersion reads major.minor out of a `docker --version` line such as
// "Docker version 29.6.2, build abc".
var dockerVersion = regexp.MustCompile(`^Docker version v?(\d+)\.(\d+)`)

// dockerWarning reports an engine too old for `host-gateway`; an unreadable
// version line warns nothing, since the audit cannot tell.
func dockerWarning(version string) string {
	m := dockerVersion.FindStringSubmatch(version)
	if m == nil {
		return ""
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > hostGatewayEngine[0] || (major == hostGatewayEngine[0] && minor >= hostGatewayEngine[1]) {
		return ""
	}
	return fmt.Sprintf("Docker %d.%d is older than %d.%d: the Gateway cannot reach the host at host.docker.internal",
		major, minor, hostGatewayEngine[0], hostGatewayEngine[1])
}

// firstLine returns the first non-empty line of output, trimmed and bounded so a
// chatty tool cannot flood the report.
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		if len(line) > 200 {
			return line[:200]
		}
		return line
	}
	return ""
}
