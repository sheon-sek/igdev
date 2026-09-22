package modules

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeModl writes a `.modl` (a zip with module.xml) into dir.
func writeModl(t *testing.T, dir, name, moduleXML string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handle, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	entry, err := writer.Create("module.xml")
	if err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(moduleXML)); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// A staged artifact reports the metadata its module.xml carries, and the shape
// the bash self-test used — a `<modules><module>` wrapper on one line — is read.
func TestScanReadsModuleXML(t *testing.T) {
	dir := t.TempDir()
	writeModl(t, dir, "test.modl", `<modules><module><id>com.example.devctl-test</id><name>Devctl Test</name><version>1.0.0</version><requiredIgnitionVersion>8.3.0</requiredIgnitionVersion></module></modules>`)
	records, fault := Scan(dir)
	if fault != nil {
		t.Fatalf("Scan: %v", fault)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}
	got := records[0]
	if got.ID != "com.example.devctl-test" || got.Name != "Devctl Test" || got.Version != "1.0.0" {
		t.Errorf("record = %+v, want the module.xml metadata", got)
	}
	if got.Artifact != "test.modl" || got.Source != Local || got.Err != "" {
		t.Errorf("record provenance = %+v, want test.modl from the local directory", got)
	}
	if !Has(records, "com.example.devctl-test") {
		t.Error("Has did not find the staged module")
	}
}

// An artifact that is not a readable archive is reported, not fatal: the listing
// still shows what is in the directory.
func TestScanReportsUnreadableArtifact(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.modl"), []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeModl(t, dir, "good.modl", `<module><id>com.example.good</id><name>Good</name><version>2.0.0</version></module>`)
	records, fault := Scan(dir)
	if fault != nil {
		t.Fatalf("Scan: %v", fault)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v, want both artifacts", records)
	}
	byArtifact := map[string]Record{}
	for _, record := range records {
		byArtifact[record.Artifact] = record
	}
	if broken := byArtifact["broken.modl"]; broken.ID != "" || broken.Err == "" {
		t.Errorf("broken record = %+v, want an unreadable artifact with its reason", broken)
	}
	if Status(byArtifact["broken.modl"], nil) != StatusUnreadable {
		t.Errorf("unreadable status = %q, want %q", Status(byArtifact["broken.modl"], nil), StatusUnreadable)
	}
	if good := byArtifact["good.modl"]; good.ID != "com.example.good" {
		t.Errorf("good record = %+v, want the readable module", good)
	}
}

// Two artifacts declaring one module id collapse, and the last file wins.
func TestScanDedupesByModuleID(t *testing.T) {
	dir := t.TempDir()
	writeModl(t, dir, "a.modl", `<module><id>com.example.dup</id><name>First</name><version>1.0.0</version></module>`)
	writeModl(t, dir, "b.modl", `<module><id>com.example.dup</id><name>Second</name><version>2.0.0</version></module>`)
	records, fault := Scan(dir)
	if fault != nil {
		t.Fatalf("Scan: %v", fault)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one row per module id", records)
	}
	if records[0].Name != "Second" {
		t.Errorf("name = %q, want the last file's metadata", records[0].Name)
	}
}

// A directory that does not exist yet is an empty listing, which is the state
// before any artifact is added.
func TestScanMissingDirectory(t *testing.T) {
	records, fault := Scan(filepath.Join(t.TempDir(), "absent"))
	if fault != nil {
		t.Fatalf("Scan: %v", fault)
	}
	if len(records) != 0 {
		t.Errorf("records = %+v, want none", records)
	}
}

// The whitelist semantics come from the bash specification: an empty list is no
// whitelist, and a non-empty list selects exactly its members.
func TestEnabled(t *testing.T) {
	if !Enabled(nil, "com.any.module") {
		t.Error("an empty whitelist did not enable a module")
	}
	if !Enabled([]string{"a", " b ", "c"}, "b") {
		t.Error("a whitelist entry with padding did not match")
	}
	if Enabled([]string{"a", "c"}, "b") {
		t.Error("a whitelist enabled a module it does not name")
	}
}

// A whitelist entry that is neither built-in nor staged is reported as missing,
// which is what `module list` shows as MISSING-ARTIFACT.
func TestMissing(t *testing.T) {
	records := []Record{{ID: "com.example.staged"}}
	builtin := func(id string) bool { return id == "com.example.builtin" }
	missing := Missing([]string{"com.example.builtin", "com.example.staged", "com.example.gone"}, builtin, records)
	if len(missing) != 1 || missing[0] != "com.example.gone" {
		t.Errorf("missing = %v, want only the unresolvable entry", missing)
	}
}

// The status vocabulary: enabled, staged but not enabled, unreadable.
func TestStatus(t *testing.T) {
	staged := Record{ID: "com.example.mod", Artifact: "mod.modl", Source: Local}
	if got := Status(staged, nil); got != StatusEnabled {
		t.Errorf("status = %q, want %q", got, StatusEnabled)
	}
	if got := Status(staged, []string{"com.example.other"}); got != StatusStagedNotEnabled {
		t.Errorf("status = %q, want %q", got, StatusStagedNotEnabled)
	}
}

// A solution-suite selector id is built-in without a catalog row.
func TestBuiltinSuiteSelector(t *testing.T) {
	inCatalog := func(id string) bool { return id == "com.example.builtin" }
	if !Builtin(inCatalog, "com.example.builtin") {
		t.Error("catalog module was not reported built-in")
	}
	if !Builtin(inCatalog, SuiteSelectorPrefix+"edge") {
		t.Error("suite selector was not reported built-in")
	}
	if Builtin(inCatalog, "com.example.private") {
		t.Error("unknown module was reported built-in")
	}
}
