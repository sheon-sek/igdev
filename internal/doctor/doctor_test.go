package doctor

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// outputFor is a runner standing in for a host: it answers from a table and
// reports everything else as "not on PATH", exactly as exec does.
func outputFor(table map[string]string) Runner {
	return func(args []string) (string, error) {
		key := strings.Join(args, " ")
		if out, ok := table[key]; ok {
			return out, nil
		}
		return "", &exec.Error{Name: args[0], Err: exec.ErrNotFound}
	}
}

func probeList() []Prerequisite {
	return []Prerequisite{
		{Name: "docker", Required: true, Args: []string{"docker", "--version"}},
		{Name: "java", Required: true, Args: []string{"java", "--version"}},
		{Name: "gradle", Required: false, Args: []string{"gradle", "--version"}},
	}
}

// Each prerequisite is reported with the version line its own command printed,
// so a reader never has to run the probe again to find out what is installed.
func TestAuditReportsVersions(t *testing.T) {
	report := Audit(outputFor(map[string]string{
		"docker --version": "Docker version 27.0.3, build abc123\n",
		"java --version":   "\nopenjdk version \"21.0.5\" 2024-10-15\n",
		"gradle --version": "\nGradle 8.5\n\nBuild time: 2023-11-17\n",
	}), probeList()...)

	if !report.Ready {
		t.Errorf("ready = false with every prerequisite present: %+v", report)
	}
	want := map[string]string{
		"docker": "Docker version 27.0.3, build abc123",
		"java":   `openjdk version "21.0.5" 2024-10-15`,
		"gradle": "Gradle 8.5",
	}
	for _, entry := range report.Prerequisites {
		if entry.State != StatePresent {
			t.Errorf("%s state = %s, want present (%s)", entry.Name, entry.State, entry.Error)
		}
		if entry.Version != want[entry.Name] {
			t.Errorf("%s version = %q, want %q", entry.Name, entry.Version, want[entry.Name])
		}
		if entry.Error != "" {
			t.Errorf("%s carries an error although present: %q", entry.Name, entry.Error)
		}
		if entry.Command == "" {
			t.Errorf("%s does not say which probe was run", entry.Name)
		}
	}
}

// A missing required prerequisite makes the host unready and says which command
// was not found; a missing optional one is information, not a refusal.
func TestAuditDistinguishesRequiredFromOptional(t *testing.T) {
	report := Audit(outputFor(map[string]string{
		"java --version": "\nopenjdk version \"21.0.5\" 2024-10-15\n",
	}), probeList()...)

	if report.Ready {
		t.Error("ready = true with docker missing")
	}
	byName := map[string]Entry{}
	for _, entry := range report.Prerequisites {
		byName[entry.Name] = entry
	}
	docker := byName["docker"]
	if docker.State != StateMissing {
		t.Errorf("docker state = %s, want missing", docker.State)
	}
	if !strings.Contains(docker.Error, "docker") || !strings.Contains(docker.Error, "PATH") {
		t.Errorf("docker error = %q, want the missing command named", docker.Error)
	}
	if docker.Version != "" {
		t.Errorf("a missing prerequisite reports version %q", docker.Version)
	}
	gradle := byName["gradle"]
	if gradle.State != StateMissing || gradle.Required {
		t.Errorf("gradle = %+v, want an optional missing entry", gradle)
	}
	if byName["java"].State != StatePresent {
		t.Errorf("java = %+v, want present", byName["java"])
	}
}

// A command that is on PATH but produces no usable version line is "failed", not
// "present": an entry always carries a version or the reason there is none.
func TestAuditReportsFailedProbes(t *testing.T) {
	broken := Runner(func(args []string) (string, error) {
		switch args[0] {
		case "docker":
			return "", errors.New("exit status 127: cannot execute binary file")
		case "java":
			return "\n", nil
		default:
			return "", &exec.Error{Name: args[0], Err: exec.ErrNotFound}
		}
	})
	report := Audit(broken, probeList()...)

	for _, entry := range report.Prerequisites {
		switch entry.Name {
		case "docker":
			if entry.State != StateFailed || !strings.Contains(entry.Error, "exit status 127") {
				t.Errorf("docker = %+v, want failed with the runner's error", entry)
			}
		case "java":
			if entry.State != StateFailed || entry.Error == "" {
				t.Errorf("java = %+v, want failed with a reason", entry)
			}
		}
		if entry.State == StatePresent && entry.Version == "" {
			t.Errorf("%s is present with no version", entry.Name)
		}
		if entry.State != StatePresent && entry.Error == "" {
			t.Errorf("%s is %s with no error", entry.Name, entry.State)
		}
	}
	if report.Ready {
		t.Error("ready = true with a failed required probe")
	}
}

// A tool that exits non-zero but still names its version is usable: the version
// line is the evidence, not the exit status.
func TestAuditAcceptsAVersionFromAFailingExit(t *testing.T) {
	runner := Runner(func(args []string) (string, error) {
		if args[0] == "docker" {
			return "Docker version 27.0.3, build abc123\n", fmt.Errorf("exit status 1")
		}
		return "", &exec.Error{Name: args[0], Err: exec.ErrNotFound}
	})
	report := Audit(runner, probeList()...)
	if got := report.Prerequisites[0]; got.State != StatePresent || got.Version != "Docker version 27.0.3, build abc123" {
		t.Errorf("docker = %+v, want present with its version line", got)
	}
}

// The default probe list is the frozen set igdev's own runtime work depends on.
func TestDefaultProbesAreFrozen(t *testing.T) {
	var names, required []string
	for _, p := range Default() {
		names = append(names, p.Name)
		if p.Required {
			required = append(required, p.Name)
		}
		if len(p.Args) == 0 || p.Args[0] == "" {
			t.Errorf("%s has no probe command", p.Name)
		}
	}
	if strings.Join(names, ",") != "docker,compose,java,gradle" {
		t.Errorf("Default() probes %v, want docker,compose,java,gradle", names)
	}
	if strings.Join(required, ",") != "docker,compose,java" {
		t.Errorf("required probes = %v, want docker,compose,java", required)
	}
}
