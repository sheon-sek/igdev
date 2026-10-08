package cleanup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/xdg"
)

const (
	nsA = "igdev-aaaaaaaa"
	nsB = "igdev-bbbbbbbb"
	nsC = "igdev-cccccccc"
	idA = "aaaaaaaa-0000-4000-8000-000000000000"
	idB = "bbbbbbbb-0000-4000-8000-000000000000"
)

// fakeEngine answers the calls cleanup makes from in-memory state, and removes
// what it is told to remove.
type fakeEngine struct {
	containers []string // name\tproject\tworking_dir\tinstance
	volumes    []string // name\tproject
	networks   []string
	images     []string
	inUse      map[string]bool // images a foreign container holds
	fail       error           // every call fails with this
	failOutput string
	calls      [][]string
}

func (f *fakeEngine) run(args ...string) (string, string, error) {
	f.calls = append(f.calls, args)
	if f.fail != nil {
		return "", f.failOutput, f.fail
	}
	switch {
	case args[0] == "ps":
		return strings.Join(f.containers, "\n"), "", nil
	case args[0] == "volume" && args[1] == "ls":
		return strings.Join(f.volumes, "\n"), "", nil
	case args[0] == "network" && args[1] == "ls":
		return strings.Join(f.networks, "\n"), "", nil
	case args[0] == "image" && args[1] == "ls":
		return strings.Join(f.images, "\n"), "", nil
	case args[0] == "rm":
		f.containers = drop(f.containers, args[len(args)-1])
	case args[0] == "volume" && args[1] == "rm":
		f.volumes = drop(f.volumes, args[len(args)-1])
	case args[0] == "network" && args[1] == "rm":
		f.networks = drop(f.networks, args[len(args)-1])
	case args[0] == "image" && args[1] == "rm":
		ref := args[len(args)-1]
		if f.inUse[ref] {
			return "", "Error response from daemon: conflict: unable to remove repository reference " + ref, errors.New("exit status 1")
		}
		f.images = drop(f.images, ref)
	}
	return "", "", nil
}

func drop(rows []string, name string) []string {
	var out []string
	for _, row := range rows {
		if strings.SplitN(row, "\t", 2)[0] != name {
			out = append(out, row)
		}
	}
	return out
}

func (f *fakeEngine) called(verb ...string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(strings.Join(call, " "), strings.Join(verb, " ")) {
			return true
		}
	}
	return false
}

// instance adds the objects compose creates for one Instance started from dir.
// Compose labels the containers with the Compose file's directory, which for
// igdev is .igdev/runtime inside the checkout.
func (f *fakeEngine) instance(ns, id, dir string) {
	dir = filepath.Join(dir, ".igdev", "runtime")
	f.containers = append(f.containers,
		ns+"-gateway-1\t"+ns+"\t"+dir+"\t"+id,
		ns+"-trial-keeper-1\t"+ns+"\t"+dir+"\t"+id)
	f.volumes = append(f.volumes, ns+"_gateway-data\t"+ns)
	f.networks = append(f.networks, ns+"_default\t"+ns)
	f.images = append(f.images, ns+":8.3.8")
}

func checkout(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "igdev.toml"), "schema = 1\n")
	write(t, filepath.Join(root, ".igdev", "setup.json"), `{"schema":1,"instance_id":"`+id+`"}`+"\n")
	write(t, filepath.Join(root, ".gitignore"), "# igdev: disposable checkout state (managed entry, do not edit)\n.igdev/\n")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func refs(plan Plan, action Action) []string {
	var out []string
	for _, item := range plan.Items {
		if item.Action == action {
			out = append(out, item.Ref)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func options(root, ns string, engine *fakeEngine) Options {
	return Options{Root: root, Namespace: ns, Docker: engine.run, Home: filepath.Join(root, "no-home"),
		Agents: AgentsBlock{File: "AGENTS.md", Start: "<!-- igdev:start -->", End: "<!-- igdev:end -->"}}
}

func TestCheckoutScopeRemovesOnlyThisInstance(t *testing.T) {
	root := checkout(t, idA)
	other := t.TempDir()
	engine := &fakeEngine{inUse: map[string]bool{}}
	engine.instance(nsA, idA, root)
	engine.instance(nsB, idB, other)
	// A user's own compose project with an igdev- name is never igdev's.
	engine.containers = append(engine.containers, "igdev-foo-web-1\tigdev-foo\t"+other+"\t")
	engine.volumes = append(engine.volumes, "igdev-foo_data\tigdev-foo")
	engine.images = append(engine.images, "inductiveautomation/ignition:8.3.8")

	plan, fault := Build(options(root, nsA, engine))
	if fault != nil {
		t.Fatal(fault)
	}
	want := []string{nsA + "-gateway-1", nsA + "-trial-keeper-1", nsA + "_default", nsA + "_gateway-data",
		nsA + ":8.3.8", filepath.Join(root, ".igdev")}
	got := refs(plan, WouldRemove)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan = %v\nwant  %v", got, want)
	}
	if engine.called("rm") {
		t.Fatal("Build removed something")
	}

	plan.Execute()
	if n := plan.Count(Removed); n != len(want) {
		t.Fatalf("removed %d items, want %d: %+v", n, len(want), plan.Items)
	}
	if exists(filepath.Join(root, ".igdev")) || !exists(filepath.Join(root, "igdev.toml")) {
		t.Fatal("checkout scope must remove .igdev/ and keep igdev.toml")
	}
	for _, row := range engine.containers {
		if strings.HasPrefix(row, nsA) {
			t.Fatalf("container left behind: %s", row)
		}
	}
	if len(engine.containers) != 3 || !contains(engine.images, "inductiveautomation/ignition:8.3.8") {
		t.Fatalf("other projects or the base image were touched: %v %v", engine.containers, engine.images)
	}

	again, fault := Build(options(root, "", engine))
	if fault != nil || again.Pending() != 0 {
		t.Fatalf("second run still has work: %+v %v", again.Items, fault)
	}
}

// A checkout whose .igdev/ is already gone still finds its Gateway through the
// working_dir label compose put on the containers.
func TestCheckoutScopeFindsInstanceWithoutSetup(t *testing.T) {
	root := t.TempDir()
	engine := &fakeEngine{}
	engine.instance(nsA, idA, root)
	plan, fault := Build(options(root, "", engine))
	if fault != nil {
		t.Fatal(fault)
	}
	if !contains(refs(plan, WouldRemove), nsA+"_gateway-data") {
		t.Fatalf("volume not found by working dir: %+v", plan.Items)
	}
}

func TestDaemonDownRemovesNothing(t *testing.T) {
	root := checkout(t, idA)
	engine := &fakeEngine{fail: errors.New("exit status 1"),
		failOutput: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}
	_, fault := Build(options(root, nsA, engine))
	if fault == nil || fault.Code != contract.CodeDockerDaemon {
		t.Fatalf("fault = %v, want %s", fault, contract.CodeDockerDaemon)
	}
	if !exists(filepath.Join(root, ".igdev")) {
		t.Fatal(".igdev/ must survive a daemon that does not answer")
	}
}

func TestNoDockerCLIStillCleansFiles(t *testing.T) {
	root := checkout(t, idA)
	engine := &fakeEngine{fail: &exec.Error{Name: "docker", Err: os.ErrNotExist}}
	plan, fault := Build(options(root, nsA, engine))
	if fault != nil {
		t.Fatal(fault)
	}
	if got := refs(plan, WouldRemove); len(got) != 1 || got[0] != filepath.Join(root, ".igdev") {
		t.Fatalf("plan = %v", got)
	}
}

func TestMachineScope(t *testing.T) {
	root := checkout(t, idA)
	other := checkout(t, idB)
	unrelated := checkout(t, "dddddddd-0000-4000-8000-000000000000")
	home := t.TempDir()
	engine := &fakeEngine{inUse: map[string]bool{"inductiveautomation/ignition:8.1.40": true}}
	engine.instance(nsA, idA, root)
	engine.instance(nsB, idB, other)
	// An orphan: only its volume is left, its checkout long gone.
	engine.volumes = append(engine.volumes, nsC+"_gateway-data\t"+nsC)
	engine.images = append(engine.images, nsB+":8.1.40",
		"inductiveautomation/ignition:8.3.8", "inductiveautomation/ignition:8.1.40", "inductiveautomation/ignition:8.0.0")
	engine.containers = append(engine.containers, "igdev-foo-web-1\tigdev-foo\t"+unrelated+"\t")

	dirs := xdg.Dir{Cache: filepath.Join(home, ".cache", "igdev"), Config: filepath.Join(home, ".config", "igdev"),
		State: filepath.Join(home, ".local", "state", "igdev")}
	write(t, filepath.Join(dirs.Cache, "update-check.json"), "{}")
	write(t, filepath.Join(dirs.Config, "accepted.toml"), "eula = true\n")
	write(t, filepath.Join(home, ".claude", "skills", "igdev", "SKILL.md"), "skill")
	write(t, filepath.Join(home, ".claude", "skills", "igdev", "references", "errors.md"), "x")
	write(t, filepath.Join(home, ".claude", "skills", "other", "SKILL.md"), "not ours")
	write(t, filepath.Join(home, ".cache", "act", "x"), "act")

	opts := options(root, nsA, engine)
	opts.Machine, opts.Home, opts.Dirs = true, home, dirs
	opts.Environ = map[string]string{"IGDEV_ACCEPT_EULA": "Y"}
	plan, fault := Build(opts)
	if fault != nil {
		t.Fatal(fault)
	}
	plan.Execute()
	removed := refs(plan, Removed)
	for _, want := range []string{nsB + "-gateway-1", nsC + "_gateway-data", nsB + ":8.1.40",
		"inductiveautomation/ignition:8.3.8", filepath.Join(other, ".igdev"), dirs.Cache, dirs.Config,
		filepath.Join(home, ".claude", "skills", "igdev")} {
		if !contains(removed, want) {
			t.Errorf("%s not removed: %v", want, removed)
		}
	}
	kept := refs(plan, Kept)
	for _, want := range []string{"inductiveautomation/ignition:8.1.40", "docker build cache",
		filepath.Join(home, ".cache", "act"), "IGDEV_ACCEPT_EULA"} {
		if !contains(kept, want) {
			t.Errorf("%s not reported kept: %v", want, kept)
		}
	}
	if !contains(removed, "inductiveautomation/ignition:8.0.0") {
		t.Error("every unused official Ignition image goes with --machine")
	}
	if !exists(filepath.Join(unrelated, ".igdev")) || len(engine.containers) != 1 {
		t.Errorf("an unrelated checkout or project was touched: %v", engine.containers)
	}
	if !exists(filepath.Join(home, ".claude", "skills", "other", "SKILL.md")) {
		t.Error("another skill was removed")
	}
	if plan.Count(Failed) != 0 {
		t.Errorf("failures: %+v", plan.Items)
	}
}

func TestDeinit(t *testing.T) {
	root := checkout(t, idA)
	gitignore := "# igdev: disposable checkout state (managed entry, do not edit)\n.igdev/\n"
	write(t, filepath.Join(root, "AGENTS.md"), "# Agents\n\nBe kind.\n\n<!-- igdev:start -->\n## igdev\n<!-- igdev:end -->\n")
	write(t, filepath.Join(root, ".claude", "skills", "igdev", "SKILL.md"), "skill")
	write(t, filepath.Join(root, ".claude", "skills", "igdev", "references", "README.md"), "x")
	write(t, filepath.Join(root, ".agents", "skills", "igdev", "SKILL.md"), "skill")
	write(t, filepath.Join(root, ".agents", "skills", "igdev", "notes.md"), "mine")
	write(t, filepath.Join(root, "catalog", "overlay.tsv"), "row\n")

	opts := options(root, nsA, &fakeEngine{})
	opts.Deinit = true
	opts.ContractTOML = []byte("schema = 1\n[catalog]\noverlay_paths = [\"catalog/overlay.tsv\"]\n")
	plan, fault := Build(opts)
	if fault != nil {
		t.Fatal(fault)
	}
	plan.Execute()
	if plan.Count(Failed) != 0 {
		t.Fatalf("failures: %+v", plan.Items)
	}
	if exists(filepath.Join(root, "igdev.toml")) || exists(filepath.Join(root, ".claude")) {
		t.Error("igdev.toml or the repo skill survived")
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(raw) != "# Agents\n\nBe kind.\n" {
		t.Errorf("AGENTS.md = %q", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(raw) != gitignore {
		t.Errorf(".gitignore changed: %q", raw)
	}
	if !exists(filepath.Join(root, ".agents", "skills", "igdev", "notes.md")) {
		t.Error("a file igdev does not own beside the skill was removed")
	}
	if !contains(refs(plan, Kept), filepath.Join(root, "catalog", "overlay.tsv")) || !exists(filepath.Join(root, "catalog")) {
		t.Error("the overlay must be reported kept and left in place")
	}
}

func TestDeinitDeletesAgentsFileHoldingOnlyTheBlock(t *testing.T) {
	root := checkout(t, idA)
	write(t, filepath.Join(root, "AGENTS.md"), "<!-- igdev:start -->\n## igdev\n<!-- igdev:end -->\n")
	opts := options(root, nsA, &fakeEngine{})
	opts.Deinit = true
	plan, _ := Build(opts)
	plan.Execute()
	if exists(filepath.Join(root, "AGENTS.md")) {
		t.Error("an AGENTS.md left empty must be removed")
	}
}

func TestUninstall(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(home, "tools")
	profile := "export EDITOR=vi\n\n" + ProfileMarker + "\n" +
		`case ":$PATH:" in *":` + prefix + `:"*) ;; *) PATH="` + prefix + `${PATH:+:$PATH}" ;; esac` + "\nexport PATH\n"
	write(t, filepath.Join(home, ".profile"), profile)
	exe := filepath.Join(prefix, "igdev")
	write(t, exe, "binary")

	opts := Options{Docker: (&fakeEngine{}).run, Home: home, Uninstall: true, Machine: true, Executable: exe}
	plan, fault := Build(opts)
	if fault != nil {
		t.Fatal(fault)
	}
	plan.Execute()
	if exists(exe) {
		t.Error("the binary under an install.sh prefix survived")
	}
	if raw, _ := os.ReadFile(filepath.Join(home, ".profile")); string(raw) != "export EDITOR=vi\n" {
		t.Errorf(".profile = %q", raw)
	}

	elsewhere := filepath.Join(t.TempDir(), "igdev")
	write(t, elsewhere, "binary")
	opts.Executable = elsewhere
	plan, _ = Build(opts)
	plan.Execute()
	if !exists(elsewhere) || !contains(refs(plan, Kept), elsewhere) {
		t.Error("a binary outside every install.sh prefix must be kept")
	}
}
