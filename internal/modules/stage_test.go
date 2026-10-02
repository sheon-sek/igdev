package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// artifact writes a `.modl` declaring id/version into dir/name.
func artifact(t *testing.T, dir, name, id, version string) string {
	t.Helper()
	writeModl(t, dir, name, "<modules><module><id>"+id+"</id><name>Sample</name><version>"+version+
		"</version></module></modules>")
	return filepath.Join(dir, name)
}

// stagedNames lists the `.modl` file names in a staging directory.
func stagedNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".modl") {
			out = append(out, entry.Name())
		}
	}
	return out
}

// A contract glob resolves against the Project Root and every match is staged
// the way `module add` stages one file, with the glob that produced it reported.
func TestStageArtifactsStagesEveryMatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".igdev", "modules")
	build := filepath.Join(root, "build")
	artifact(t, build, "com.acme.vision-1.2.3.modl", "com.acme.vision", "1.2.3")
	artifact(t, build, "com.acme.plc-2.0.0.modl", "com.acme.plc", "2.0.0")

	staged, fault := StageArtifacts(root, dir, []string{"build/*.modl"})
	if fault != nil {
		t.Fatalf("StageArtifacts: %v", fault)
	}
	if len(staged) != 2 {
		t.Fatalf("staged %d artifacts, want both matches: %+v", len(staged), staged)
	}
	if got := strings.Join(stagedNames(t, dir), ","); got != "com.acme.plc-2.0.0.modl,com.acme.vision-1.2.3.modl" {
		t.Errorf("staging directory holds %q, want both artifacts", got)
	}
	for _, entry := range staged {
		if entry.Glob != "build/*.modl" {
			t.Errorf("%s reports glob %q, want the contract pattern", entry.Staged.Artifact, entry.Glob)
		}
		if entry.Staged.Replaced || len(entry.Superseded) != 0 {
			t.Errorf("%s replaced %v on a first staging", entry.Staged.Artifact, entry.Superseded)
		}
		if entry.Staged.Bytes == 0 {
			t.Errorf("%s reports no bytes staged", entry.Staged.Artifact)
		}
	}
	if staged[0].Staged.ID != "com.acme.plc" || staged[1].Staged.ID != "com.acme.vision" {
		t.Errorf("ids = %s, %s, want the sorted matches' own ids", staged[0].Staged.ID, staged[1].Staged.ID)
	}
}

// A version bump changes the file name and keeps the module id, so the previous
// build is removed instead of joining the new one in the mount the Gateway reads.
func TestStageArtifactsReplacesTheSameModuleID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".igdev", "modules")
	previous := filepath.Join(dir, "com.acme.vision-1.2.3.modl")
	artifact(t, dir, "com.acme.vision-1.2.3.modl", "com.acme.vision", "1.2.3")
	artifact(t, filepath.Join(root, "build"), "com.acme.vision-1.2.4.modl", "com.acme.vision", "1.2.4")

	staged, fault := StageArtifacts(root, dir, []string{"build/*.modl"})
	if fault != nil {
		t.Fatalf("StageArtifacts: %v", fault)
	}
	if len(staged) != 1 || staged[0].Staged.Version != "1.2.4" {
		t.Fatalf("staged = %+v, want the new build alone", staged)
	}
	if got := strings.Join(staged[0].Superseded, ","); got != "com.acme.vision-1.2.3.modl" {
		t.Errorf("superseded = %q, want the previous build", got)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Errorf("the previous build is still staged: %v", err)
	}
	if got := strings.Join(stagedNames(t, dir), ","); got != "com.acme.vision-1.2.4.modl" {
		t.Errorf("staging directory holds %q, want one artifact per module id", got)
	}
}

// A declared glob that matches nothing is a fault, not a silent success: the
// contract says what the build produces, and the fix is named.
func TestStageArtifactsRefusesAnEmptyMatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".igdev", "modules")
	artifact(t, filepath.Join(root, "build"), "com.acme.vision-1.2.3.modl", "com.acme.vision", "1.2.3")
	// A directory matches the glob and is still not an artifact.
	if err := os.MkdirAll(filepath.Join(root, "build", "sub.modl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, fault := StageArtifacts(root, dir, []string{"build/*.modl", "dist/*.modl"})
	if fault == nil {
		t.Fatal("a glob that matched nothing staged successfully")
	}
	if fault.Code != contract.CodeModuleArtifactMissing {
		t.Errorf("code = %s, want %s", fault.Code, contract.CodeModuleArtifactMissing)
	}
	if !strings.Contains(fault.Message, "dist/*.modl") {
		t.Errorf("fault does not name the glob: %q", fault.Message)
	}
	if len(fault.Remediation) == 0 {
		t.Error("fault carries no remediation")
	}
}

// A contract that declares no globs stages nothing on its own: the staging
// directory is not even created.
func TestStageArtifactsWithoutGlobsStagesNothing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".igdev", "modules")
	staged, fault := StageArtifacts(root, dir, nil)
	if fault != nil || staged != nil {
		t.Fatalf("StageArtifacts(nil) = %v, %v; want nothing and no fault", staged, fault)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("staging directory was created: %v", err)
	}
}
