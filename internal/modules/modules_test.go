package modules

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
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

// A staged artifact reports the metadata its module.xml carries, and the
// single-line `<modules><module>` wrapper shape is read.
func TestScanReadsModuleXML(t *testing.T) {
	dir := t.TempDir()
	writeModl(t, dir, "test.modl", `<modules><module><id>com.example.sample-test</id><name>Sample Test</name><version>1.0.0</version><requiredIgnitionVersion>8.3.0</requiredIgnitionVersion></module></modules>`)
	records, fault := Scan(dir)
	if fault != nil {
		t.Fatalf("Scan: %v", fault)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}
	got := records[0]
	if got.ID != "com.example.sample-test" || got.Name != "Sample Test" || got.Version != "1.0.0" {
		t.Errorf("record = %+v, want the module.xml metadata", got)
	}
	if got.Artifact != "test.modl" || got.Source != Local || got.Err != "" {
		t.Errorf("record provenance = %+v, want test.modl from the local directory", got)
	}
	if !Has(records, "com.example.sample-test") {
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

// writeModlPayload writes a `.modl` whose module.xml entry carries body.
func writeModlPayload(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	entry, err := writer.Create("module.xml")
	if err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(body)); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// Validate is what `module add` gates on: the file has to be a `.modl` whose
// module.xml yields an id the Project Contract would accept. Each refusal names
// the check that failed, and the zip-bomb guard refuses an archive whose declared
// expansion is not that of a module archive at all.
func TestValidateRejectsNonModuleArchives(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name     string
		file     string
		body     string
		contains string
	}{
		{
			name:     "wrong extension",
			file:     "module.zip",
			body:     `<module><id>com.example.a</id></module>`,
			contains: "does not end in .modl",
		},
		{
			name:     "no id",
			file:     "no-id.modl",
			body:     `<module><name>Nameless</name></module>`,
			contains: "carries no <id>",
		},
		{
			name:     "id that is not a module id",
			file:     "bad-id.modl",
			body:     `<module><id>has space</id></module>`,
			contains: "is not a module id",
		},
	} {
		path := filepath.Join(dir, tc.file)
		writeModlPayload(t, path, tc.body)
		_, fault := Validate(path)
		if fault == nil {
			t.Errorf("%s: Validate accepted the archive", tc.name)
			continue
		}
		if fault.Code != contract.CodeModuleArchiveInvalid {
			t.Errorf("%s: code = %s, want %s", tc.name, fault.Code, contract.CodeModuleArchiveInvalid)
		}
		if !strings.Contains(fault.Message, tc.contains) {
			t.Errorf("%s: message %q does not carry %q", tc.name, fault.Message, tc.contains)
		}
	}

	// A file that is not a zip at all.
	plain := filepath.Join(dir, "plain.modl")
	if err := os.WriteFile(plain, []byte("not a zip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, fault := Validate(plain); fault == nil || !strings.Contains(fault.Message, "not a valid zip file") {
		t.Errorf("Validate(plain file) = %v", fault)
	}
	if _, fault := Validate(filepath.Join(dir, "missing.modl")); fault == nil {
		t.Error("Validate accepted a path that does not exist")
	}

	// A readable archive with the right metadata is accepted, and the record is
	// what the listing would show.
	good := filepath.Join(dir, "good.modl")
	writeModlPayload(t, good, `<modules><module><id>com.example.good</id><name>Good</name><version>1.0.0</version></module></modules>`)
	record, fault := Validate(good)
	if fault != nil {
		t.Fatalf("Validate(good) = %v", fault)
	}
	if record.ID != "com.example.good" || record.Name != "Good" || record.Version != "1.0.0" {
		t.Errorf("Validate(good) = %+v", record)
	}
	if record.Artifact != "good.modl" || record.Source != Local {
		t.Errorf("Validate(good) did not describe the file: %+v", record)
	}
}

// The guard refuses an archive that would expand past the limits, whether by
// declared size or by ratio, and it does so from the central directory: nothing
// is decompressed to make the decision.
func TestValidateRefusesZipBombs(t *testing.T) {
	dir := t.TempDir()
	compressible := filepath.Join(dir, "ratio.modl")
	writeModlPayload(t, compressible, `<module><id>com.example.bomb</id><name>`+strings.Repeat("A", 512<<10))
	if _, fault := Validate(compressible); fault == nil || !strings.Contains(fault.Message, "decompression bomb") {
		t.Errorf("Validate(ratio bomb) = %v", fault)
	}

	incompressible := filepath.Join(dir, "size.modl")
	payload := make([]byte, 2<<20)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	writeModlPayload(t, incompressible, `<module><id>com.example.big</id><name>`+string(payload))
	if _, fault := Validate(incompressible); fault == nil || !strings.Contains(fault.Message, "metadata limit") {
		t.Errorf("Validate(size bomb) = %v", fault)
	}
}

// The suggestion a fault carries is derived from the segment that names the
// module, so a typo inside a name is caught and an unrelated vendor's id is not
// offered as a hint.
func TestSuggestRanksByModuleName(t *testing.T) {
	known := []string{
		"com.example.perspective",
		"com.example.phone-notification",
		"com.example.alarm-notification",
		"com.other.vision",
	}
	if got := Suggest(known, "com.example.perspectiv", 3); len(got) != 1 || got[0] != "com.example.perspective" {
		t.Errorf("Suggest(typo) = %v, want the one close id", got)
	}
	if got := Suggest(known, "com.other.visoin", 3); len(got) != 1 || got[0] != "com.other.vision" {
		t.Errorf("Suggest(misspelled module name) = %v", got)
	}
	if got := Suggest(known, "com.acme.unknown", 3); len(got) != 0 {
		t.Errorf("Suggest(unrelated vendor) = %v, want no hint", got)
	}
	if got := Suggest(known, "com.example.perspective", 0); got != nil {
		t.Errorf("Suggest with no room = %v, want nil", got)
	}
}
