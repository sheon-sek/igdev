package itest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// catalog import-openapi ports the retired bash generator: it reads a Gateway
// /openapi.json snapshot, maps every operation to its owner the way that generator
// mapped it, and writes the REST plane of the tracked Project Overlay. These tests
// assert the whole observable behaviour through seam S1: the exit level, the JSON
// envelope, the overlay bytes, the printed diff, and what `module require` and
// `catalog status` then resolve.

// openAPIFixture reads one OpenAPI document from testdata/openapi.
func openAPIFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "openapi", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

// importState is the data member of a `catalog import-openapi` envelope.
type importState struct {
	IgnitionVersion string `json:"ignition_version"`
	Source          struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"source"`
	Output struct {
		Path     string `json:"path"`
		Action   string `json:"action"`
		Digest   string `json:"digest"`
		Declared bool   `json:"declared"`
		Diff     string `json:"diff"`
	} `json:"output"`
	Operations int `json:"operations"`
	Written    int `json:"written"`
	Added      int `json:"added"`
	Preserved  int `json:"preserved"`
	Removed    int `json:"removed"`
	Redundant  int `json:"redundant"`
}

// importStateOf decodes the data member of an import envelope.
func importStateOf(t *testing.T, stdout string) importState {
	t.Helper()
	var data importState
	testrig.DataOf(t, stdout, &data)
	return data
}

// importFixture writes one OpenAPI document into a fresh fixture project and
// returns the Project Root, the declared overlay path, and the document's digest.
func importFixture(t *testing.T, env *testrig.Env, fixture string) (dir, overlay, digest string) {
	t.Helper()
	dir = env.Project("repo", knowledgeContract("catalog/rest-overlay.tsv"))
	document := openAPIFixture(t, fixture)
	env.Write("repo/openapi.json", document)
	sum := sha256.Sum256([]byte(document))
	return dir, env.Path("repo", "catalog", "rest-overlay.tsv"), hex.EncodeToString(sum[:])
}

// The import writes the tracked overlay: the generated REST rows, the source
// digest in the header, and nothing else — the native function and capability
// rule planes are not derivable from an OpenAPI document. The imported endpoints
// then resolve through the overlay layer, which is the round trip the ticket is
// about.
func TestCatalogImportOpenAPIWritesTrackedOverlay(t *testing.T) {
	env := testrig.NewEnv(t)
	dir, overlayPath, digest := importFixture(t, env, "gateway.json")

	res := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "catalog_import.json", res.Stdout)
	res.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)

	data := importStateOf(t, res.Stdout)
	if data.IgnitionVersion != "8.3.8" {
		t.Errorf("ignition_version = %q, want the resolved contract version", data.IgnitionVersion)
	}
	if data.Source.Path != "openapi.json" || data.Source.SHA256 != "sha256:"+digest {
		t.Errorf("source = %+v, want the document and its digest", data.Source)
	}
	if data.Output.Path != "catalog/rest-overlay.tsv" || data.Output.Action != "created" || !data.Output.Declared {
		t.Errorf("output = %+v, want the declared overlay path created", data.Output)
	}
	// 14 operations: 6 the Core Catalog already carries identically (and so has
	// nothing to add to) and 8 the overlay contributes.
	if data.Operations != 14 || data.Redundant != 6 {
		t.Errorf("operations = %d, redundant = %d; want 14 and 6", data.Operations, data.Redundant)
	}
	if data.Written != 8 || data.Added != 8 || data.Preserved != 0 || data.Removed != 0 {
		t.Errorf("written = %d, added = %d, preserved = %d, removed = %d; want 8, 8, 0, 0",
			data.Written, data.Added, data.Preserved, data.Removed)
	}
	if data.Output.Digest != "sha256:"+digestOf(t, overlayPath) {
		t.Errorf("output digest = %q, want the digest of the file it wrote", data.Output.Digest)
	}
	if !strings.Contains(data.Output.Diff, "+++ b/catalog/rest-overlay.tsv") {
		t.Errorf("diff does not name the overlay file:\n%s", data.Output.Diff)
	}

	// The overlay file is one generated REST block: the header records the source
	// digest, the zone directive names the plane, and no other plane is claimed.
	body := readFile(t, overlayPath)
	if !strings.Contains(body, "# source_sha256="+digest) {
		t.Errorf("overlay does not record the source digest:\n%s", body)
	}
	if !strings.Contains(body, "# plane: rest") {
		t.Errorf("overlay has no REST plane directive:\n%s", body)
	}
	for _, row := range []string{
		"POST\t/data/api/v1/resources/com.inductiveautomation.opcua.drivers.bacnet/IpDeviceConfig\tmodule\tcom.inductiveautomation.opcua.drivers.bacnet,com.inductiveautomation.opcua",
		"GET\t/data/api/v1/resources/com.inductiveautomation.sip-notification/voice-settings\tmodule\tcom.inductiveautomation.phone-notification",
		"GET\t/data/api/v1/system/status\tplatform\t-",
		"DELETE\t/data/mcp/api/v1/tools/{toolName}\tprivate-module\tcom.inductiveautomation.mcp",
		"POST\t/data/reporting/api/v1/reports/scheduled\tmodule\tcom.inductiveautomation.reporting",
		"POST\t/data/reporting/api/v1/reports/{reportPath}\tmodule\tcom.inductiveautomation.reporting",
	} {
		if !strings.Contains(body, row+"\n") {
			t.Errorf("overlay is missing the row:\n  %s\n--- overlay ---\n%s", row, body)
		}
	}
	// The rows the Core Catalog already carries are not copied into the overlay:
	// a duplicate would be refused as a conflict on the next read.
	if strings.Contains(body, "GET\t/data/api/v1/gateway-info\t") {
		t.Errorf("overlay repeats a Core Catalog row:\n%s", body)
	}

	// The round trip: every imported endpoint resolves, and it says so — the
	// deciding row is the overlay's, not the core's.
	writeModl(t, env.Path("repo", ".igdev", "modules"), "mcp.modl",
		`<modules><module><id>com.inductiveautomation.mcp</id><name>MCP</name><version>1.0.0</version></module></modules>`)
	for _, tc := range []struct {
		capability string
		modules    string
	}{
		{capability: "GET /data/reporting/api/v1/reports/scheduled", modules: "com.inductiveautomation.reporting"},
		{capability: "POST /data/reporting/api/v1/reports/acme", modules: "com.inductiveautomation.reporting"},
		{capability: "GET /data/api/v1/resources/com.inductiveautomation.sip-notification/voice-settings", modules: "com.inductiveautomation.phone-notification"},
		{capability: "GET /data/api/v1/system/status", modules: ""},
		{capability: "POST /data/mcp/api/v1/tools/echo", modules: "com.inductiveautomation.mcp"},
	} {
		got := env.RunIn(dir, "module", "require", tc.capability, "--json")
		testrig.WantExit(t, got, contract.ExitOK)
		entry := require(t, got)
		if entry.Layer != "overlay" {
			t.Errorf("%s layer = %q, want overlay", tc.capability, entry.Layer)
		}
		if joined := strings.Join(entry.Modules, ","); joined != tc.modules {
			t.Errorf("%s modules = %q, want %q", tc.capability, joined, tc.modules)
		}
	}
	// A row the document only repeats from the core still resolves from the core.
	coreRow := env.RunIn(dir, "module", "require", "GET /data/api/v1/gateway-info", "--json")
	testrig.WantExit(t, coreRow, contract.ExitOK)
	if entry := require(t, coreRow); entry.Layer != "core" {
		t.Errorf("gateway-info layer = %q, want core", entry.Layer)
	}

	// catalog status reports the overlay layer with its digest and the effective
	// sum, which is the other half of the round trip.
	status := env.RunIn(dir, "catalog", "status", "--json")
	testrig.WantExit(t, status, contract.ExitOK)
	env.Golden(t, "catalog_import_status.json", status.Stdout)
	var catalogData catalogStatusData
	testrig.DataOf(t, status.Stdout, &catalogData)
	if strings.Join(catalogData.Overlay.Paths, ",") != "catalog/rest-overlay.tsv" {
		t.Errorf("overlay paths = %v, want the imported file", catalogData.Overlay.Paths)
	}
	if catalogData.Overlay.Counts.RestOperations != data.Written {
		t.Errorf("overlay REST rows = %d, want the %d the import reported",
			catalogData.Overlay.Counts.RestOperations, data.Written)
	}
	if catalogData.Effective.RestOperations != catalogData.Core.Counts.RestOperations+data.Written {
		t.Errorf("effective REST rows = %d, want the core rows plus the imported ones",
			catalogData.Effective.RestOperations)
	}
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaksOutside(t, dir)
}

// The human dialect prints the unified diff on stderr and one summary on stdout,
// so a reviewed import is visible before it is committed.
func TestCatalogImportOpenAPIHumanOutput(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo-human", knowledgeContract("catalog/rest-overlay.tsv"))
	env.Write("repo-human/openapi.json", openAPIFixture(t, "gateway.json"))

	res := env.RunIn(dir, "catalog", "import-openapi", "openapi.json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "catalog_import.txt", res.Stdout)
	env.Golden(t, "catalog_import_diff.txt", res.Stderr)
	if !strings.Contains(res.Stderr, "# plane: rest") {
		t.Errorf("the printed diff does not show the generated rows:\n%s", res.Stderr)
	}
	res.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)
}

// A second import of the same document is a no-op: byte-identical content is left
// alone, so the command reports `unchanged` and writes nothing at all. An extended
// document updates the overlay and its recorded digest, keeps the rows it no
// longer declares, and `--prune` is the opt-in that drops them.
func TestCatalogImportOpenAPIIsIdempotent(t *testing.T) {
	env := testrig.NewEnv(t)
	dir, overlayPath, _ := importFixture(t, env, "gateway.json")

	testrig.WantExit(t, env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json"), contract.ExitOK)
	created := readFile(t, overlayPath)

	again := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, again, contract.ExitOK)
	again.AssertNoLeaks(t) // nothing was written, anywhere
	repeat := importStateOf(t, again.Stdout)
	if repeat.Output.Action != "unchanged" || repeat.Output.Diff != "" {
		t.Errorf("re-import = %q with diff %q, want an unchanged no-op", repeat.Output.Action, repeat.Output.Diff)
	}
	if readFile(t, overlayPath) != created {
		t.Error("the no-op re-import rewrote the overlay")
	}

	// An extended snapshot: one route added, two the gateway no longer serves.
	env.Write("repo/openapi.json", openAPIFixture(t, "gateway-extended.json"))
	extended := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, extended, contract.ExitOK)
	env.Golden(t, "catalog_import_extended.json", extended.Stdout)
	state := importStateOf(t, extended.Stdout)
	if state.Output.Action != "updated" || state.Operations != 13 {
		t.Errorf("extended import = %q over %d operations, want an update of 13", state.Output.Action, state.Operations)
	}
	if state.Written != 7 || state.Added != 1 || state.Preserved != 2 || state.Removed != 0 {
		t.Errorf("written = %d, added = %d, preserved = %d, removed = %d; want 7, 1, 2, 0",
			state.Written, state.Added, state.Preserved, state.Removed)
	}
	body := readFile(t, overlayPath)
	if !strings.Contains(body, "/data/api/v1/system/overview") {
		t.Errorf("the added route is missing:\n%s", body)
	}
	if !strings.Contains(body, "/data/mcp/api/v1/tools/{toolName}") {
		t.Errorf("a row the document no longer declares was dropped without --prune:\n%s", body)
	}
	if !strings.Contains(body, "# source_sha256="+state.Source.SHA256[len("sha256:"):]) {
		t.Errorf("the header does not record the new source digest:\n%s", body)
	}

	// The extended document is a no-op on the second import too.
	settled := readFile(t, overlayPath)
	stable := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, stable, contract.ExitOK)
	stable.AssertNoLeaks(t)
	if state := importStateOf(t, stable.Stdout); state.Output.Action != "unchanged" {
		t.Errorf("re-import of the extended document = %q, want unchanged", state.Output.Action)
	}
	if readFile(t, overlayPath) != settled {
		t.Error("the no-op re-import rewrote the overlay")
	}

	// --prune is the opt-in that drops what the document no longer declares, and
	// the dropped endpoint stops resolving.
	pruned := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--prune", "--json")
	testrig.WantExit(t, pruned, contract.ExitOK)
	env.Golden(t, "catalog_import_pruned.json", pruned.Stdout)
	state = importStateOf(t, pruned.Stdout)
	if state.Output.Action != "updated" || state.Written != 7 || state.Added != 0 || state.Preserved != 0 || state.Removed != 2 {
		t.Errorf("pruned = %+v; want an update of 7 rows, 2 removed", state)
	}
	body = readFile(t, overlayPath)
	if strings.Contains(body, "/data/mcp/") {
		t.Errorf("--prune left a row the document does not declare:\n%s", body)
	}
	unknown := env.RunIn(dir, "module", "require", "POST /data/mcp/api/v1/tools/echo", "--json")
	testrig.WantExit(t, unknown, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, unknown.Stdout), contract.CodeUnknownCapability)
	resolved := env.RunIn(dir, "module", "require", "POST /data/reporting/api/v1/reports/acme", "--json")
	testrig.WantExit(t, resolved, contract.ExitOK)
	pruned.AssertNoLeaksOutside(t, dir)
	assertNoLitter(t, env)
}

// A row the import would write that shadows a Core Catalog row is
// IGDEV_E_OVERLAY_CONFLICT and the run stops before anything is written: the
// overlay that exists stays byte-identical.
func TestCatalogImportOpenAPIConflictFailsClosed(t *testing.T) {
	env := testrig.NewEnv(t)
	dir, overlayPath, _ := importFixture(t, env, "gateway.json")
	env.Write("repo/catalog/rest-overlay.tsv",
		"# hand-authored rest row that shadows the Core Catalog\n# plane: rest\nGET\t/data/api/v1/gateway-info\tmodule\tcom.acme.hijack\n")
	before := readFile(t, overlayPath)

	res := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "catalog_import_conflict.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeOverlayConflict)
	for _, want := range []string{"com.acme.hijack", "GET /data/api/v1/gateway-info", "platform"} {
		if !strings.Contains(envelope.Message, want) {
			t.Errorf("message = %q, want it to name %q", envelope.Message, want)
		}
	}
	if readFile(t, overlayPath) != before {
		t.Error("a conflicting import rewrote the overlay")
	}
	res.AssertNoLeaks(t)
}

// An OpenAPI document the tool cannot read, or one without an object-valued
// `paths` member, is a usage error naming the file: the input is unusable, so
// there is nothing to import and nothing is written.
func TestCatalogImportOpenAPIMalformedDocument(t *testing.T) {
	env := testrig.NewEnv(t)
	dir, overlayPath, _ := importFixture(t, env, "gateway.json")

	for _, tc := range []struct {
		name     string
		arg      string
		document string
		dir      bool
		message  string
	}{
		{name: "broken json", arg: "malformed.json",
			document: openAPIFixture(t, "malformed.json"), message: "is not valid JSON"},
		{name: "no paths", arg: "no-paths.json",
			document: openAPIFixture(t, "no-paths.json"), message: "object-valued `paths` member"},
		{name: "empty paths", arg: "empty-paths.json",
			document: openAPIFixture(t, "empty-paths.json"), message: "declares no REST operations"},
		{name: "missing file", arg: "absent.json", message: "cannot be read"},
		{name: "directory", arg: "specdir", dir: true, message: "cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch {
			case tc.dir:
				env.Mkdir("repo/" + tc.arg)
			case tc.document != "":
				env.Write("repo/"+tc.arg, tc.document)
			}
			res := env.RunIn(dir, "catalog", "import-openapi", tc.arg, "--json")
			testrig.WantExit(t, res, contract.ExitUsage)
			envelope := testrig.Envelope(t, res.Stdout)
			testrig.WantCode(t, envelope, contract.CodeUsage)
			if !strings.Contains(envelope.Message, tc.message) {
				t.Errorf("message = %q, want it to contain %q", envelope.Message, tc.message)
			}
			if !strings.Contains(envelope.Message, tc.arg) {
				t.Errorf("message = %q, want it to name %q", envelope.Message, tc.arg)
			}
			res.AssertNoLeaks(t)
			if _, err := os.Stat(overlayPath); !os.IsNotExist(err) {
				t.Error("a malformed document wrote an overlay")
			}
		})
	}

	res := env.RunIn(dir, "catalog", "import-openapi", "malformed.json", "--json")
	env.Golden(t, "catalog_import_malformed.json", res.Stdout)
}

// The Ignition version decides which Core Catalog the import resolves conflicts
// and redundancy against, so a version with no embedded catalog fails closed —
// exactly as catalog status does — and a value that is not a version at all is a
// usage error. The output path has to be repository-relative and inside the
// Project Root.
func TestCatalogImportOpenAPIUsageErrors(t *testing.T) {
	env := testrig.NewEnv(t)
	dir, overlayPath, _ := importFixture(t, env, "gateway.json")

	res := env.RunIn(dir, "catalog", "import-openapi", "openapi.json", "--ignition-version", "8.1.21", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "catalog_import_version_missing.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeCatalogVersionMissing)
	if !strings.Contains(envelope.Message, "8.3.8") {
		t.Errorf("message = %q, want the carried version named", envelope.Message)
	}
	res.AssertNoLeaks(t)

	for _, tc := range []struct {
		name   string
		args   []string
		code   contract.Code
		exit   contract.Exit
		substr string
	}{
		{name: "not a version", args: []string{"import-openapi", "openapi.json", "--ignition-version", "banana"},
			code: contract.CodeUsage, exit: contract.ExitUsage, substr: "banana"},
		{name: "absolute output", args: []string{"import-openapi", "openapi.json", "--output", "/tmp/overlay.tsv"},
			code: contract.CodeUsage, exit: contract.ExitUsage, substr: "repository-relative"},
		{name: "escaping output", args: []string{"import-openapi", "openapi.json", "--output", "../overlay.tsv"},
			code: contract.CodeUsage, exit: contract.ExitUsage, substr: "Project Root"},
		{name: "no document", args: []string{"import-openapi"},
			code: contract.CodeMissingArgument, exit: contract.ExitUsage, substr: "openapi.json"},
		{name: "two documents", args: []string{"import-openapi", "openapi.json", "no-paths.json"},
			code: contract.CodeUsage, exit: contract.ExitUsage, substr: "1 argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := env.RunIn(dir, append(append([]string{"catalog"}, tc.args...), "--json")...)
			testrig.WantExit(t, got, tc.exit)
			envelope := testrig.Envelope(t, got.Stdout)
			testrig.WantCode(t, envelope, tc.code)
			if !strings.Contains(envelope.Message, tc.substr) {
				t.Errorf("message = %q, want it to contain %q", envelope.Message, tc.substr)
			}
			got.AssertNoLeaks(t)
		})
	}
	if _, err := os.Stat(overlayPath); !os.IsNotExist(err) {
		t.Error("a refused invocation wrote an overlay")
	}

	// Outside a Project Root there is no tracked overlay to write.
	plain := env.Project("plain", "")
	env.Write("plain/openapi.json", openAPIFixture(t, "gateway.json"))
	outside := env.RunIn(plain, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, outside, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, outside.Stdout), contract.CodeNotInitialized)
	outside.AssertNoLeaks(t)
}

// The output path defaults to the first declared overlay file, and an import that
// writes a file nothing declares warns on stderr — a tracked overlay the contract
// does not name is never resolved, so the round trip would silently stop working.
func TestCatalogImportOpenAPIOutputTarget(t *testing.T) {
	env := testrig.NewEnv(t)
	declared := env.Project("declared", knowledgeContract("catalog/acme.tsv", "catalog/extra.tsv"))
	env.Write("declared/openapi.json", openAPIFixture(t, "gateway.json"))
	env.Write("declared/catalog/extra.tsv", "# a second declared overlay, empty for now\n")

	res := env.RunIn(declared, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	res.AssertNoLeaksOutside(t, declared)
	if data := importStateOf(t, res.Stdout); data.Output.Path != "catalog/acme.tsv" || !data.Output.Declared {
		t.Errorf("output = %+v, want the first declared overlay path", data.Output)
	}
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want no warning for a declared output", res.Stderr)
	}
	resolved := env.RunIn(declared, "module", "require", "GET /data/api/v1/system/status", "--json")
	testrig.WantExit(t, resolved, contract.ExitOK)
	resolved.AssertNoLeaks(t)

	undeclared := env.Project("undeclared", knowledgeContract())
	env.Write("undeclared/openapi.json", openAPIFixture(t, "gateway.json"))
	fallback := env.RunIn(undeclared, "catalog", "import-openapi", "openapi.json", "--json")
	testrig.WantExit(t, fallback, contract.ExitOK)
	fallback.AssertNoLeaksOutside(t, undeclared)
	env.Golden(t, "catalog_import_undeclared_stderr.txt", fallback.Stderr)
	data := importStateOf(t, fallback.Stdout)
	if data.Output.Path != "catalog/rest-overlay.tsv" || data.Output.Declared {
		t.Errorf("output = %+v, want the fallback path reported as undeclared", data.Output)
	}

	status := env.RunIn(undeclared, "catalog", "status", "--json")
	testrig.WantExit(t, status, contract.ExitOK)
	status.AssertNoLeaks(t)
	var catalogData catalogStatusData
	testrig.DataOf(t, status.Stdout, &catalogData)
	if len(catalogData.Overlay.Paths) != 0 {
		t.Errorf("overlay paths = %v, want none declared", catalogData.Overlay.Paths)
	}
	assertNoLitter(t, env)
}

// digestOf is the sha256 of a file the run wrote, without its sha256: prefix.
func digestOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
