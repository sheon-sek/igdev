package itest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// knowledgeContract is the fixture contract the knowledge verbs resolve against:
// a project root, the declared scan paths, and an optional overlay.
func knowledgeContract(overlayPaths ...string) string {
	body := `schema = 1

[project]
name = "fixture"

[ignition]
version = "8.3.8"
jython_version = "2.7.4"
edition = "standard"

[modules]
enabled = []
`
	if len(overlayPaths) > 0 {
		quoted := make([]string, 0, len(overlayPaths))
		for _, path := range overlayPaths {
			quoted = append(quoted, `"`+path+`"`)
		}
		body += "\n[catalog]\noverlay_paths = [" + strings.Join(quoted, ", ") + "]\n"
	}
	return body + `
[scan]
jython = ["src"]
capabilities = ["src"]

[gateway]
memory_mb = 2048
timezone = "UTC"
`
}

// requireEntry is one element of `module require --json` data.
type requireEntry struct {
	Capability string   `json:"capability"`
	Kind       string   `json:"kind"`
	Platform   bool     `json:"platform"`
	Modules    []string `json:"modules"`
	Layer      string   `json:"layer"`
	Class      string   `json:"class,omitempty"`
	Note       string   `json:"note,omitempty"`
}

type requireData struct {
	Capabilities []requireEntry `json:"capabilities"`
}

// scanEntry is one element of `module scan --json` data.
type scanEntry struct {
	Capability string   `json:"capability"`
	Kind       string   `json:"kind"`
	Platform   bool     `json:"platform"`
	Modules    []string `json:"modules"`
	Layer      string   `json:"layer"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
}

type scanData struct {
	Checked  int         `json:"checked"`
	Findings []scanEntry `json:"findings"`
}

type builtInEntry struct {
	ID       string `json:"id"`
	Artifact string `json:"artifact"`
	Enabled  bool   `json:"enabled"`
}

type privateEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Artifact string `json:"artifact"`
	Source   string `json:"source"`
	Status   string `json:"status"`
}

type moduleListData struct {
	IgnitionVersion string         `json:"ignition_version"`
	EnabledAll      bool           `json:"enabled_all"`
	Whitelist       []string       `json:"whitelist"`
	BuiltIn         []builtInEntry `json:"built_in"`
	Private         []privateEntry `json:"private"`
}

// catalogLayer mirrors the `catalog status` layer shape.
type catalogLayer struct {
	Version string         `json:"version"`
	Source  string         `json:"source"`
	Digest  string         `json:"digest"`
	Paths   []string       `json:"paths"`
	Counts  catalog.Counts `json:"counts"`
}

type catalogStatusData struct {
	IgnitionVersion string         `json:"ignition_version"`
	Core            catalogLayer   `json:"core"`
	Overlay         catalogLayer   `json:"overlay"`
	Effective       catalog.Counts `json:"effective"`
}

// require resolves one capability through the binary, failing the test when the
// entry is absent.
func require(t *testing.T, res testrig.Result) requireEntry {
	t.Helper()
	var data requireData
	testrig.DataOf(t, res.Stdout, &data)
	if len(data.Capabilities) != 1 {
		t.Fatalf("capabilities = %+v, want exactly one", data.Capabilities)
	}
	return data.Capabilities[0]
}

// writeModl stages a `.modl` in a checkout's private module directory.
func writeModl(t *testing.T, dir, name, moduleXML string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
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

// The named mapping anchors from the legacy self-test, resolved through the real
// binary: each one is an acceptance anchor for the ported capability table.
func TestKnowledgeAnchorMappings(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())
	// opccom, twilio, and secsgem are licensed modules that do not ship in the
	// image; the anchors below check the mapping, so their artifacts are staged.
	for _, id := range []string{
		"com.inductiveautomation.opccom",
		"com.inductiveautomation.twilio",
		"com.inductiveautomation.secsgem",
	} {
		writeModl(t, env.Path("repo", ".igdev", "modules"), strings.TrimPrefix(id, "com.inductiveautomation.")+".modl",
			`<module><id>`+id+`</id><name>fixture</name><version>1.0.0</version></module>`)
	}
	anchors := []struct {
		capability string
		platform   bool
		modules    string
	}{
		{capability: "system.tag.readBlocking", platform: true},
		{capability: "system.serial.openSerialPort", platform: true},
		{capability: "system.report.executeReport", modules: "com.inductiveautomation.reporting"},
		{capability: "system.security.validateUser", modules: "com.inductiveautomation.vision"},
		{capability: "system.groups.loadFromFile", modules: "com.inductiveautomation.sqlbridge"},
		{capability: "system.roster.getRosters", modules: "com.inductiveautomation.alarm-notification"},
		{capability: "system.opchda.readRaw", modules: "com.inductiveautomation.opccom"},
		{capability: "system.twilio.sendSms", modules: "com.inductiveautomation.twilio"},
		{capability: "system.secsgem.sendRequest", modules: "com.inductiveautomation.secsgem"},
		{capability: "system.device.addDevice", modules: "com.inductiveautomation.opcua"},
	}
	for _, anchor := range anchors {
		res := env.RunIn(root, "module", "require", anchor.capability, "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		entry := require(t, res)
		if entry.Platform != anchor.platform {
			t.Errorf("%s platform = %v, want %v", anchor.capability, entry.Platform, anchor.platform)
		}
		if got := strings.Join(entry.Modules, ","); got != anchor.modules {
			t.Errorf("%s modules = %q, want %q", anchor.capability, got, anchor.modules)
		}
		if entry.Layer != "core" {
			t.Errorf("%s layer = %q, want core", anchor.capability, entry.Layer)
		}
	}
}

// require on a module-owned function is ok when the whitelist enables the module,
// and IGDEV_E_MODULE_NOT_ENABLED with the enable command as Remediation when it
// does not.
func TestModuleRequireWhitelist(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	res := env.RunIn(root, "module", "require", "system.report.executeReport", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_require_module_owned.json", res.Stdout)
	if entry := require(t, res); entry.Modules[0] != "com.inductiveautomation.reporting" {
		t.Errorf("modules = %v, want the reporting module", entry.Modules)
	}

	env.Write("repo/igdev.toml", strings.Replace(knowledgeContract(),
		"enabled = []", `enabled = ["com.inductiveautomation.perspective"]`, 1))
	res = env.RunIn(root, "module", "require", "system.report.executeReport", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleNotEnabled)
	testrig.WantRemediation(t, envelope, "igdev module enable com.inductiveautomation.reporting")
	env.Golden(t, "module_require_not_enabled.json", res.Stdout)

	// The human dialect names the same failure and the same fix.
	res = env.RunIn(root, "module", "require", "system.report.executeReport")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "module_require_not_enabled.txt", res.Stderr)
	res.AssertNoLeaks(t)
}

// The platform case keeps the legacy preflight line verbatim.
func TestModuleRequireHumanOutput(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	res := env.RunIn(root, "module", "require", "system.tag.readBlocking")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_require_platform.txt", res.Stdout)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}

	// A conditional function carries its note, in the NOTE vocabulary.
	res = env.RunIn(root, "module", "require", "system.device.addDevice")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_require_conditional.txt", res.Stdout)
	if !strings.Contains(res.Stdout, "[module-preflight] NOTE:") {
		t.Errorf("conditional capability printed no note:\n%s", res.Stdout)
	}
}

// An unknown capability fails with IGDEV_E_UNKNOWN_CAPABILITY at exit 1, in both
// dialects, and the message keeps the plane-specific wording.
func TestModuleRequireUnknownCapability(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	for _, tc := range []struct {
		name       string
		capability string
		want       string
	}{
		{name: "function", capability: "system.tag.readBlokcing", want: "unknown or non-Gateway Ignition 8.3 native function"},
		{name: "rest path", capability: "/data/api/v1/not-a-real-endpoint", want: "REST operation is not present in the Ignition 8.3.8 catalog"},
		{name: "rest method", capability: "POST /data/api/v1/gateway-info", want: "REST operation is not present in the Ignition 8.3.8 catalog"},
		{name: "alias", capability: "acme.unknown.thing", want: "no mapping for acme.unknown.thing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.RunIn(root, "module", "require", tc.capability, "--json")
			testrig.WantExit(t, res, contract.ExitFailure)
			envelope := testrig.Envelope(t, res.Stdout)
			testrig.WantCode(t, envelope, contract.CodeUnknownCapability)
			if !strings.Contains(envelope.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", envelope.Message, tc.want)
			}
		})
	}
	res := env.RunIn(root, "module", "require", "system.tag.readBlokcing")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "module_require_unknown.txt", res.Stderr)
	res.AssertNoLeaks(t)
}

// REST ownership resolves including the alias rows, a method-resolved request,
// and the trailing-slash template; the method is case-insensitive.
func TestModuleRequireRESTOwnership(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	for _, tc := range []struct {
		capability string
		platform   bool
		modules    string
	}{
		{capability: "GET /data/reporting/api/v1/reports/current", modules: "com.inductiveautomation.reporting"},
		{capability: "get /data/api/v1/gateway-info", platform: true},
		{capability: "/data/api/v1/resources/names/com.inductiveautomation.opcua/device", modules: "com.inductiveautomation.opcua"},
		{capability: "PUT /data/api/v1/resources/com.inductiveautomation.sip-notification/script-settings", modules: "com.inductiveautomation.phone-notification"},
		{capability: "GET /data/perspective/api/v1/sessions/", modules: "com.inductiveautomation.perspective"},
	} {
		res := env.RunIn(root, "module", "require", tc.capability, "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		entry := require(t, res)
		if entry.Platform != tc.platform {
			t.Errorf("%s platform = %v, want %v", tc.capability, entry.Platform, tc.platform)
		}
		if got := strings.Join(entry.Modules, ","); got != tc.modules {
			t.Errorf("%s modules = %q, want %q", tc.capability, got, tc.modules)
		}
		if entry.Kind != "rest" {
			t.Errorf("%s kind = %q, want rest", tc.capability, entry.Kind)
		}
	}
}

// A private-module endpoint resolves, and is satisfied only once an artifact
// declaring that module is staged in the checkout.
func TestModuleRequirePrivateModuleEndpoint(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())
	writeModl(t, env.Path("repo", ".igdev", "modules"), "acme.modl",
		`<modules><module><id>com.acme.widgets</id><name>Acme Widgets</name><version>1.2.3</version></module></modules>`)

	res := env.RunIn(root, "module", "require", "module:com.acme.widgets", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	entry := require(t, res)
	if entry.Kind != "module" || strings.Join(entry.Modules, ",") != "com.acme.widgets" {
		t.Errorf("entry = %+v, want the staged private module", entry)
	}

	// A whitelisted module with no artifact fails with the staging command.
	env.Write("repo/igdev.toml", strings.Replace(knowledgeContract(),
		"enabled = []", `enabled = ["com.acme.other"]`, 1))
	res = env.RunIn(root, "module", "require", "module:com.acme.other", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleArtifactMissing)
	testrig.WantRemediation(t, envelope, "igdev module add /path/to/module.modl")
	res.AssertNoLeaks(t)
}

// scan finds nested references in a fixture tree and reports file:line, and the
// contract's [scan].capabilities paths are the default.
func TestModuleScanReportsFileLine(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())
	env.Write("repo/src/handlers.py", strings.Join([]string{
		"system.historian.types.dataPoint(1, 2, 3)",
		"system.tag.readBlocking([])",
		"client.get('GET /data/api/v1/gateway-info')",
		"backup = '/data/perspective/api/v1/sessions/'",
	}, "\n")+"\n")
	env.Write("repo/src/nested/deep.js", "system.alarm.getRosters()\n")

	res := env.RunIn(root, "module", "scan", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_scan_findings.json", res.Stdout)

	var data scanData
	testrig.DataOf(t, res.Stdout, &data)
	type location struct {
		file string
		line int
	}
	want := map[string]location{
		"system.historian.types.dataPoint":   {"src/handlers.py", 1},
		"system.tag.readBlocking":            {"src/handlers.py", 2},
		"GET /data/api/v1/gateway-info":      {"src/handlers.py", 3},
		"/data/perspective/api/v1/sessions/": {"src/handlers.py", 4},
		"system.alarm.getRosters":            {"src/nested/deep.js", 1},
	}
	got := map[string]scanEntry{}
	for _, finding := range data.Findings {
		got[finding.Capability] = finding
	}
	if len(got) != len(want) {
		t.Errorf("findings = %+v, want %d entries", data.Findings, len(want))
	}
	for capability, expected := range want {
		finding, ok := got[capability]
		if !ok {
			t.Errorf("finding for %q is missing", capability)
			continue
		}
		if finding.File != expected.file || finding.Line != expected.line {
			t.Errorf("%s = %s:%d, want %s:%d", capability, finding.File, finding.Line, expected.file, expected.line)
		}
	}
	if data.Checked != len(want) {
		t.Errorf("checked = %d, want the %d distinct capabilities", data.Checked, len(want))
	}
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)
}

// scan fails, with the file:line of every offending reference, when a required
// module is not enabled; and it warns about a path that does not exist instead of
// failing on it.
func TestModuleScanFailures(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", strings.Replace(knowledgeContract(),
		"enabled = []", `enabled = ["com.inductiveautomation.perspective"]`, 1))
	env.Write("repo/src/report.py", "system.report.executeReport('a', {}, {})\n")

	res := env.RunIn(root, "module", "scan", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleNotEnabled)
	if !strings.Contains(envelope.Message, "src/report.py:1") {
		t.Errorf("message = %q, want the file:line of the offending reference", envelope.Message)
	}

	res = env.RunIn(root, "module", "scan", "src")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "module_scan_not_enabled.txt", res.Stderr)

	res = env.RunIn(root, "module", "scan", "src", "absent")
	testrig.WantExit(t, res, contract.ExitFailure)
	if !strings.Contains(res.Stderr, "module scan path missing: absent") {
		t.Errorf("stderr = %q, want the missing-path warning", res.Stderr)
	}
	res.AssertNoLeaks(t)
}

// module list reports the built-in group, the private artifacts, and the
// whitelist; the human dialect keeps the legacy group headings and status
// vocabulary.
func TestModuleList(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	res := env.RunIn(root, "module", "list", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_list.json", res.Stdout)
	var data moduleListData
	testrig.DataOf(t, res.Stdout, &data)
	if !data.EnabledAll {
		t.Errorf("enabled_all = false for an empty whitelist, want true")
	}
	if data.IgnitionVersion != "8.3.8" {
		t.Errorf("ignition_version = %q, want 8.3.8", data.IgnitionVersion)
	}
	if len(data.BuiltIn) == 0 || len(data.Private) != 0 {
		t.Errorf("groups = %d built-in, %d private; want built-ins and no private artifacts", len(data.BuiltIn), len(data.Private))
	}
	for _, row := range data.BuiltIn {
		if row.ID == "com.inductiveautomation.perspective" && row.Artifact != "Perspective-module.modl" {
			t.Errorf("perspective artifact = %q, want the catalog's file name", row.Artifact)
		}
		if !row.Enabled {
			t.Errorf("%s is not enabled under an empty whitelist", row.ID)
		}
	}

	res = env.RunIn(root, "module", "list", "--built-in")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_list_built_in.txt", res.Stdout)

	res = env.RunIn(root, "module", "list", "--private")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_list_private.txt", res.Stdout)

	// One staged artifact, and one whitelisted module with no artifact.
	writeModl(t, env.Path("repo", ".igdev", "modules"), "acme.modl",
		`<modules><module><id>com.acme.widgets</id><name>Acme Widgets</name><version>1.2.3</version></module></modules>`)
	env.Write("repo/igdev.toml", strings.Replace(knowledgeContract(),
		"enabled = []", `enabled = ["com.acme.widgets", "com.acme.absent"]`, 1))
	res = env.RunIn(root, "module", "list", "--private", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_list_private.json", res.Stdout)
	testrig.DataOf(t, res.Stdout, &data)
	statuses := map[string]string{}
	for _, row := range data.Private {
		statuses[row.ID] = row.Status
	}
	if statuses["com.acme.widgets"] != "enabled" {
		t.Errorf("staged artifact status = %q, want enabled", statuses["com.acme.widgets"])
	}
	if statuses["com.acme.absent"] != "MISSING-ARTIFACT" {
		t.Errorf("whitelisted-but-absent status = %q, want MISSING-ARTIFACT", statuses["com.acme.absent"])
	}

	res = env.RunIn(root, "module", "list", "--built-in", "--private")
	testrig.WantExit(t, res, contract.ExitUsage)
	res.AssertNoLeaks(t)
}

// catalog status reports both layers with digests, and the core digest is the
// compiled-in integrity constant: a byte change to an embedded catalog file
// fails this test, which is what the ADR asks for instead of row counts.
func TestCatalogStatusLayers(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())
	env.Write("repo/catalog/overlay.tsv", overlayFile)
	env.Write("repo/igdev.toml", knowledgeContract("catalog/overlay.tsv"))

	res := env.RunIn(root, "catalog", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "catalog_status.json", res.Stdout)

	var data catalogStatusData
	testrig.DataOf(t, res.Stdout, &data)
	wantCore, fault := catalog.CoreDigest("8.3.8")
	if fault != nil {
		t.Fatalf("CoreDigest: %v", fault)
	}
	if data.Core.Digest != wantCore {
		t.Errorf("core digest = %q, want the embedded constant %q", data.Core.Digest, wantCore)
	}
	if data.Core.Source != "embedded" {
		t.Errorf("core source = %q, want embedded", data.Core.Source)
	}
	if !strings.HasPrefix(data.Overlay.Digest, "sha256:") || data.Overlay.Digest == wantCore {
		t.Errorf("overlay digest = %q, want its own layer digest", data.Overlay.Digest)
	}
	if strings.Join(data.Overlay.Paths, ",") != "catalog/overlay.tsv" {
		t.Errorf("overlay paths = %v, want the declared file", data.Overlay.Paths)
	}
	if data.Overlay.Counts.RestOperations != 1 || data.Overlay.Counts.NativeFunctions != 1 || data.Overlay.Counts.CapabilityRules != 1 {
		t.Errorf("overlay counts = %+v, want one row per plane", data.Overlay.Counts)
	}
	if data.Effective.RestOperations != data.Core.Counts.RestOperations+1 {
		t.Errorf("effective REST operations = %d, want the core rows plus the overlay's", data.Effective.RestOperations)
	}

	res = env.RunIn(root, "catalog", "status")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "catalog_status.txt", res.Stdout)
	env.AssertNoDockerCalls(t)
	res.AssertNoLeaks(t)
}

// overlayFile is a hand-written Project Overlay: one row per knowledge plane.
const overlayFile = `# igdev Project Overlay for this repository.
# plane: rest
GET	/data/acme/api/v1/widgets	module	com.acme.widgets
# plane: native-function
system.acme.widget.ping	module	com.acme.widgets	
# plane: capability-rule
prefix	acme.widget.	com.acme.widgets	Acme widget scripting helpers
`

// A hand-written overlay adds an endpoint and a function without a new binary:
// require resolves them from the overlay layer, and catalog status reports the
// overlay digest. Staging the artifact the overlay names makes the requirement
// satisfiable.
func TestOverlayAddsCapabilities(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract("catalog/overlay.tsv"))
	env.Write("repo/catalog/overlay.tsv", overlayFile)
	writeModl(t, env.Path("repo", ".igdev", "modules"), "acme.modl",
		`<modules><module><id>com.acme.widgets</id><name>Acme Widgets</name><version>1.2.3</version></module></modules>`)

	for _, tc := range []struct {
		capability string
		kind       string
	}{
		{capability: "GET /data/acme/api/v1/widgets", kind: "rest"},
		{capability: "system.acme.widget.ping", kind: "native-function"},
		{capability: "acme.widget.load", kind: "alias"},
	} {
		res := env.RunIn(root, "module", "require", tc.capability, "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		entry := require(t, res)
		if entry.Layer != "overlay" {
			t.Errorf("%s layer = %q, want overlay", tc.capability, entry.Layer)
		}
		if entry.Kind != tc.kind {
			t.Errorf("%s kind = %q, want %q", tc.capability, entry.Kind, tc.kind)
		}
	}
	overlayRequire := env.RunIn(root, "module", "require", "GET /data/acme/api/v1/widgets", "--json")
	testrig.WantExit(t, overlayRequire, contract.ExitOK)
	env.Golden(t, "module_require_overlay.json", overlayRequire.Stdout)

	// The overlay is scanned like any other capability source.
	env.Write("repo/src/widgets.py", "system.acme.widget.ping('GET /data/acme/api/v1/widgets')\n")
	res := env.RunIn(root, "module", "scan", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_scan_overlay.json", res.Stdout)
	res.AssertNoLeaks(t)
}

// An overlay row that shadows a Core Catalog row is a fault naming both rows.
func TestOverlayConflictFailsClosed(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract("catalog/overlay.tsv"))
	env.Write("repo/catalog/overlay.tsv", "# plane: rest\nGET\t/data/api/v1/gateway-info\tmodule\tcom.acme.hijack\n")

	res := env.RunIn(root, "catalog", "status", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeOverlayConflict)
	for _, want := range []string{"com.acme.hijack", "platform", "GET /data/api/v1/gateway-info"} {
		if !strings.Contains(envelope.Message, want) {
			t.Errorf("message = %q, want it to name %q", envelope.Message, want)
		}
	}
	// The same fault reaches every knowledge verb, not just catalog status.
	res = env.RunIn(root, "module", "require", "system.tag.readBlocking")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "overlay_conflict.txt", res.Stderr)
	res.AssertNoLeaks(t)
}

// A malformed overlay file is the project's to fix, and the message names the
// file and line.
func TestOverlayInvalidFailsClosed(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract("catalog/overlay.tsv"))
	env.Write("repo/catalog/overlay.tsv", "GET\t/data/acme/x\tmodule\tcom.acme\n")

	res := env.RunIn(root, "catalog", "status", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeOverlayInvalid)
	if !strings.Contains(envelope.Message, "catalog/overlay.tsv") || !strings.Contains(envelope.Message, "line 1") {
		t.Errorf("message = %q, want the file and line", envelope.Message)
	}
	res.AssertNoLeaks(t)
}

// An Ignition version with no embedded catalog fails closed rather than guessing.
func TestCatalogVersionMissing(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	res := env.RunIn(root, "catalog", "status", "--config", "ignition.version=8.1.21", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeCatalogVersionMissing)
	if !strings.Contains(envelope.Message, "8.3.8") {
		t.Errorf("message = %q, want the carried version named", envelope.Message)
	}
	env.Golden(t, "catalog_version_missing.json", res.Stdout)

	res = env.RunIn(root, "module", "require", "system.tag.readBlocking", "--config", "ignition.version=8.1.21", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeCatalogVersionMissing)
	res.AssertNoLeaks(t)
}

// catalog status works before init — which versions this binary carries is a
// property of the binary — while the module verbs need a Project Contract for its
// whitelist and overlay.
func TestKnowledgeOutsideProject(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("plain", "")

	res := env.RunIn(dir, "catalog", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data catalogStatusData
	testrig.DataOf(t, res.Stdout, &data)
	if data.Core.Digest == "" || len(data.Overlay.Paths) != 0 {
		t.Errorf("outside a project = %+v, want the core layer alone", data)
	}

	res = env.RunIn(dir, "module", "require", "system.tag.readBlocking", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeNotInitialized)

	res = env.RunIn(dir, "module", "list", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeNotInitialized)
	res.AssertNoLeaks(t)
}

// require and scan need a capability; scan falls back to the contract's declared
// scan paths, so a missing one is a usage error naming the flag to add.
func TestKnowledgeUsageErrors(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())

	res := env.RunIn(root, "module", "require", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeMissingArgument)

	noScan := env.Project("bare", "schema = 1\n\n[project]\nname = \"no-scan\"\n")
	res = env.RunIn(noScan, "module", "scan", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeMissingArgument)
	res.AssertNoLeaks(t)
}

// The knowledge verbs pass the Gate's contract stages but not the Setup Stamp:
// capability knowledge does not depend on a materialized checkout.
func TestKnowledgeWorksBeforeSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	root := env.Project("repo", knowledgeContract())
	if _, err := os.Stat(env.Path("repo", ".igdev", "setup.json")); err == nil {
		t.Fatal("fixture unexpectedly carries a Checkout Setup")
	}
	res := env.RunIn(root, "module", "require", "system.tag.readBlocking", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	// A contract this CLI cannot read is still refused, and so is a broken one.
	env.Write("repo/igdev.toml", "schema = 1\n\n[project\nname = broken\n")
	res = env.RunIn(root, "module", "require", "system.tag.readBlocking", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeConfigInvalid)
	res.AssertNoLeaks(t)
}
