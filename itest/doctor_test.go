package itest

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// doctor audits the host, read-only: it reports each prerequisite with the
// version line it printed or the reason it could not be read, and it never fails
// the command, so an agent always receives the report itself.

// Every prerequisite is reported, and every entry carries either a version or an
// error. The version comes from the tool's own command, which the rig's docker
// shim can answer for.
func TestDoctorReportsPrerequisites(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.SetBaseEnv("IGDEV_SHIM_DOCKER_OUT=Docker version 27.0.3, build abc123")

	res := env.MustRun("doctor", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}

	var report struct {
		Ready         bool `json:"ready"`
		Prerequisites []struct {
			Name     string `json:"name"`
			Command  string `json:"command"`
			Required bool   `json:"required"`
			State    string `json:"state"`
			Version  string `json:"version"`
			Error    string `json:"error"`
		} `json:"prerequisites"`
	}
	testrig.DataOf(t, res.Stdout, &report)

	byName := map[string]int{}
	for i, entry := range report.Prerequisites {
		byName[entry.Name] = i
		if entry.Command == "" {
			t.Errorf("%s does not say which probe it ran", entry.Name)
		}
		switch entry.State {
		case "present":
			if entry.Version == "" || entry.Error != "" {
				t.Errorf("%s is present with version %q and error %q", entry.Name, entry.Version, entry.Error)
			}
		case "missing", "failed":
			if entry.Error == "" {
				t.Errorf("%s is %s without a reason", entry.Name, entry.State)
			}
		default:
			t.Errorf("%s has unknown state %q", entry.Name, entry.State)
		}
	}
	for _, name := range []string{"docker", "compose", "java", "gradle"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("doctor does not audit %s:\n%s", name, res.Stdout)
		}
	}
	// The shim answers both docker probes with the canned version line, so the
	// version igdev reports is the version the tool printed.
	for _, name := range []string{"docker", "compose"} {
		entry := report.Prerequisites[byName[name]]
		if entry.State != "present" || entry.Version != "Docker version 27.0.3, build abc123" {
			t.Errorf("%s = %+v, want present with the shim's version line", name, entry)
		}
	}
	// ready is the required subset: docker and compose are present here, so it
	// depends only on the JVM this machine happens to have.
	requiredReady := true
	for _, entry := range report.Prerequisites {
		if entry.Required && entry.State != "present" {
			requiredReady = false
		}
	}
	if report.Ready != requiredReady {
		t.Errorf("ready = %v, want %v for these entries", report.Ready, requiredReady)
	}
}

// A host with nothing on PATH reports every prerequisite as missing, naming the
// command that was not found. It is still a successful command: the report is the
// payload.
func TestDoctorOnAnEmptyPATH(t *testing.T) {
	env := testrig.NewEnv(t)
	empty := env.Mkdir("empty-path")

	res := env.Run(testrig.Run{Args: []string{"doctor", "--json"}, Env: []string{"PATH=" + empty}})
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "doctor_missing.json", res.Stdout)

	var report struct {
		Ready         bool `json:"ready"`
		Prerequisites []struct {
			Name    string `json:"name"`
			State   string `json:"state"`
			Version string `json:"version"`
			Error   string `json:"error"`
		} `json:"prerequisites"`
	}
	testrig.DataOf(t, res.Stdout, &report)
	if report.Ready {
		t.Error("ready = true with nothing on PATH")
	}
	if len(report.Prerequisites) == 0 {
		t.Fatal("doctor reported no prerequisites")
	}
	for _, entry := range report.Prerequisites {
		if entry.State != "missing" {
			t.Errorf("%s state = %s, want missing", entry.Name, entry.State)
		}
		if !strings.Contains(entry.Error, "PATH") {
			t.Errorf("%s error = %q, want it to name the missing command", entry.Name, entry.Error)
		}
		if entry.Version != "" {
			t.Errorf("%s reports version %q while missing", entry.Name, entry.Version)
		}
	}

	human := env.Run(testrig.Run{Args: []string{"doctor"}, Env: []string{"PATH=" + empty}})
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "doctor_missing.txt", human.Stdout)
}

// Probing the host creates nothing: no file, no container, no process beyond the
// probes themselves. An empty PATH makes the probe set deterministic and keeps
// the assertion meaningful.
func TestDoctorTouchesNothing(t *testing.T) {
	env := testrig.NewEnv(t)
	empty := env.Mkdir("empty-path")
	root := env.Project("repo", testrig.MinimalContract)
	base := env.Snapshot()

	res := env.Run(testrig.Run{Args: []string{"doctor", "--json"}, Dir: root, Env: []string{"PATH=" + empty}})
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaks(t)
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("doctor changed %v", changes)
	}
	env.AssertNoDockerCalls(t)
}
