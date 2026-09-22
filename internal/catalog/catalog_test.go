package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// testSet answers the module questions from fixed sets, so capability resolution
// is tested without a checkout.
type testSet struct {
	enabled  []string
	builtin  []string
	artifact []string
}

func (s testSet) has(list []string, id string) bool {
	for _, candidate := range list {
		if candidate == id {
			return true
		}
	}
	return false
}

func (s testSet) Enabled(id string) bool  { return s.has(s.enabled, id) }
func (s testSet) Builtin(id string) bool  { return s.has(s.builtin, id) }
func (s testSet) Artifact(id string) bool { return s.has(s.artifact, id) }

// everything is the checkout whose whitelist names every built-in module: a
// module-owned capability is satisfied exactly when the module ships in the
// image, which is what a default `igdev init` contract enables.
func everything(cap *Effective) testSet {
	var ids []string
	for _, module := range cap.BuiltinModules() {
		ids = append(ids, module.ID)
	}
	return testSet{enabled: ids, builtin: ids}
}

// defaultEffective is the Core Catalog with no overlay, the tuple every anchor
// below resolves against.
func defaultEffective(t *testing.T) *Effective {
	t.Helper()
	eff, err := New(Target, nil)
	if err != nil {
		t.Fatalf("New(%s): %v", Target, err)
	}
	return eff
}

// The named mapping anchors from the bash self-test, which is the executable
// specification for this table. Every row is one behavior, not one count.
func TestAnchorMappings(t *testing.T) {
	eff := defaultEffective(t)
	for _, tc := range []struct {
		capability string
		platform   bool
		modules    []string
	}{
		// platform: ships with the Gateway, no optional module.
		{capability: "system.tag.readBlocking", platform: true},
		{capability: "system.serial.openSerialPort", platform: true},
		// module-owned functions.
		{capability: "system.report.executeReport", modules: []string{"com.inductiveautomation.reporting"}},
		{capability: "system.report.executeReport", modules: []string{"com.inductiveautomation.reporting"}},
		{capability: "system.security.validateUser", modules: []string{"com.inductiveautomation.vision"}},
		{capability: "system.vision.logout", modules: []string{"com.inductiveautomation.vision"}},
		{capability: "system.groups.loadFromFile", modules: []string{"com.inductiveautomation.sqlbridge"}},
		{capability: "system.roster.getRosters", modules: []string{"com.inductiveautomation.alarm-notification"}},
		{capability: "system.alarm.createRoster", modules: []string{"com.inductiveautomation.alarm-notification"}},
		{capability: "system.opchda.readRaw", modules: []string{"com.inductiveautomation.opccom"}},
		{capability: "system.twilio.sendSms", modules: []string{"com.inductiveautomation.twilio"}},
		{capability: "system.secsgem.sendRequest", modules: []string{"com.inductiveautomation.secsgem"}},
		{capability: "system.device.addDevice", modules: []string{"com.inductiveautomation.opcua"}},
		{capability: "system.historian.types.dataPoint", modules: []string{"com.inductiveautomation.historian"}},
		// REST ownership, including the platform, a nested path, an alias, and
		// the trailing-slash template.
		{capability: "/data/api/v1/resources/names/com.inductiveautomation.opcua/device", modules: []string{"com.inductiveautomation.opcua"}},
		{capability: "GET /data/reporting/api/v1/reports/current", modules: []string{"com.inductiveautomation.reporting"}},
		{capability: "get /data/api/v1/gateway-info", platform: true},
		{capability: "PUT /data/api/v1/resources/com.inductiveautomation.sip-notification/script-settings", modules: []string{"com.inductiveautomation.phone-notification"}},
		{capability: "GET /data/perspective/api/v1/sessions/", modules: []string{"com.inductiveautomation.perspective"}},
		{capability: "GET /data/perspective/api/v1/sessions", modules: []string{"com.inductiveautomation.perspective"}},
		// An explicit module capability.
		{capability: "module:com.inductiveautomation.reporting", modules: []string{"com.inductiveautomation.reporting"}},
		{capability: "com.inductiveautomation.opcua", modules: []string{"com.inductiveautomation.opcua"}},
	} {
		res, err := eff.Resolve(tc.capability)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.capability, err)
			continue
		}
		if res.Platform != tc.platform {
			t.Errorf("Resolve(%q).Platform = %v, want %v", tc.capability, res.Platform, tc.platform)
		}
		if strings.Join(res.Modules, ",") != strings.Join(tc.modules, ",") {
			t.Errorf("Resolve(%q).Modules = %v, want %v", tc.capability, res.Modules, tc.modules)
		}
		if res.Layer != LayerCore {
			t.Errorf("Resolve(%q).Layer = %q, want core", tc.capability, res.Layer)
		}
	}
}

// A conditional function carries its note, which is the reason the classification
// exists.
func TestConditionalFunctionNote(t *testing.T) {
	eff := defaultEffective(t)
	res, err := eff.Resolve("system.device.addDevice")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Class != ClassConditional {
		t.Errorf("class = %q, want %q", res.Class, ClassConditional)
	}
	if !strings.Contains(res.Note, "OPC UA base required") {
		t.Errorf("note = %q, want the catalog's note", res.Note)
	}
}

// Unknown capabilities fail closed with the code and wording the bash
// specification printed.
func TestUnknownCapabilities(t *testing.T) {
	eff := defaultEffective(t)
	for _, capability := range []string{
		"system.tag.readBlokcing",
		"/data/api/v1/not-a-real-endpoint",
		"POST /data/api/v1/gateway-info",
		"POST /data/not-a-real-endpoint",
		"some.unknown.thing",
	} {
		_, err := eff.Resolve(capability)
		if err == nil {
			t.Errorf("Resolve(%q) succeeded, want IGDEV_E_UNKNOWN_CAPABILITY", capability)
			continue
		}
		if err.Code != contract.CodeUnknownCapability {
			t.Errorf("Resolve(%q) code = %q, want %q", capability, err.Code, contract.CodeUnknownCapability)
		}
		if err.Exit != contract.ExitFailure {
			t.Errorf("Resolve(%q) exit = %d, want %d", capability, err.Exit, contract.ExitFailure)
		}
	}
	if _, err := eff.Resolve("system.tag.readBlokcing"); !strings.Contains(err.Message, "unknown or non-Gateway Ignition 8.3 native function") {
		t.Errorf("native function message = %q, want the legacy wording", err.Message)
	}
	if _, err := eff.Resolve("/data/api/v1/not-a-real-endpoint"); !strings.Contains(err.Message, "REST operation is not present in the Ignition 8.3.8 catalog") {
		t.Errorf("REST message = %q, want the legacy wording", err.Message)
	}
	if _, err := eff.Resolve("some.unknown.thing"); err.Message != "no mapping for some.unknown.thing" {
		t.Errorf("alias message = %q, want the legacy wording", err.Message)
	}
}

// require's verdicts: a platform capability needs nothing, an enabled module is
// ok, a whitelisted-away module is IGDEV_E_MODULE_NOT_ENABLED with the enabling
// command, and an enabled module with no artifact is
// IGDEV_E_MODULE_ARTIFACT_MISSING.
func TestVerifyModuleVerdicts(t *testing.T) {
	eff := defaultEffective(t)
	all := everything(eff)

	if _, err := eff.Verify("system.tag.readBlocking", all); err != nil {
		t.Errorf("platform capability failed: %v", err)
	}
	if _, err := eff.Verify("system.report.executeReport", all); err != nil {
		t.Errorf("built-in module capability failed: %v", err)
	}

	none := testSet{}
	_, err := eff.Verify("system.report.executeReport", none)
	if err == nil {
		t.Fatal("a capability outside the whitelist passed")
	}
	if err.Code != contract.CodeModuleNotEnabled {
		t.Errorf("code = %q, want %q", err.Code, contract.CodeModuleNotEnabled)
	}
	found := false
	for _, step := range err.Remediation {
		if step.Command == "igdev module enable com.inductiveautomation.reporting" {
			found = true
		}
	}
	if !found {
		t.Errorf("remediation = %v, want the enable command", err.Remediation)
	}

	private := testSet{enabled: []string{"com.acme.private"}}
	_, err = eff.Verify("module:com.acme.private", private)
	if err == nil {
		t.Fatal("a private module with no artifact passed")
	}
	if err.Code != contract.CodeModuleArtifactMissing {
		t.Errorf("code = %q, want %q", err.Code, contract.CodeModuleArtifactMissing)
	}
	if len(err.Remediation) == 0 || !strings.Contains(err.Remediation[0].Command, "igdev module add") {
		t.Errorf("remediation = %v, want the add-artifact command", err.Remediation)
	}

	withFile := testSet{enabled: []string{"com.acme.private"}, artifact: []string{"com.acme.private"}}
	if _, err := eff.Verify("module:com.acme.private", withFile); err != nil {
		t.Errorf("a staged private module failed: %v", err)
	}
}

// The Core Catalog's per-version integrity is its digest. The constants below are
// the frozen values of the embedded 8.3.8 data: any byte change to a catalog file
// fails this test, which is what the ADR asks for instead of row counts.
func TestCoreDigestConstants(t *testing.T) {
	got, err := CoreDigest("8.3.8")
	if err != nil {
		t.Fatalf("CoreDigest: %v", err)
	}
	const want = "sha256:32f9ea556ff8bd14075ced23160e4c3b83131eec5f73713769c6c5254cf6ab0d"
	if got != want {
		t.Errorf("CoreDigest(8.3.8) = %q, want %q", got, want)
	}
}

// An Ignition version with no embedded data fails closed, and the message names
// the versions the binary does carry.
func TestUnknownVersionFailsClosed(t *testing.T) {
	_, err := Core("8.1.21")
	if err == nil {
		t.Fatal("Core(8.1.21) succeeded, want IGDEV_E_CATALOG_VERSION_MISSING")
	}
	if err.Code != contract.CodeCatalogVersionMissing {
		t.Errorf("code = %q, want %q", err.Code, contract.CodeCatalogVersionMissing)
	}
	if !strings.Contains(err.Message, "8.3.8") {
		t.Errorf("message = %q, want the carried versions named", err.Message)
	}
}

// writeOverlay writes an overlay file inside a scratch root and returns its
// declared path.
func writeOverlay(t *testing.T, body string) (root, declared string) {
	t.Helper()
	root = t.TempDir()
	declared = "catalog/overlay.tsv"
	full := filepath.Join(root, declared)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	return root, declared
}

// loadOverlay is the two-step load the commands perform.
func loadOverlay(t *testing.T, root string, paths []string) *Effective {
	t.Helper()
	overlay, err := LoadOverlay(root, paths)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	eff, err := New(Target, overlay)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eff
}

// A hand-written overlay adds a REST endpoint, a native function, and a
// capability rule; each resolves with the overlay layer, and the digest that
// catalog status reports comes from the file bytes.
func TestOverlayAddsRows(t *testing.T) {
	root, declared := writeOverlay(t, `# igdev Project Overlay
# plane: rest
GET	/data/acme/api/v1/widgets	module	com.acme.widgets
# plane: native-function
system.acme.widget.ping	module	com.acme.widgets	
# plane: capability-rule
prefix	acme.widget.	com.acme.widgets	Acme widget scripting helpers
`)
	eff := loadOverlay(t, root, []string{declared})

	for _, tc := range []struct {
		capability string
		modules    []string
		kind       Kind
	}{
		{capability: "GET /data/acme/api/v1/widgets", modules: []string{"com.acme.widgets"}, kind: KindREST},
		{capability: "system.acme.widget.ping", modules: []string{"com.acme.widgets"}, kind: KindNativeFunction},
		{capability: "acme.widget.load", modules: []string{"com.acme.widgets"}, kind: KindAlias},
	} {
		res, err := eff.Resolve(tc.capability)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.capability, err)
			continue
		}
		if res.Layer != LayerOverlay {
			t.Errorf("Resolve(%q).Layer = %q, want overlay", tc.capability, res.Layer)
		}
		if res.Kind != tc.kind {
			t.Errorf("Resolve(%q).Kind = %q, want %q", tc.capability, res.Kind, tc.kind)
		}
		if strings.Join(res.Modules, ",") != strings.Join(tc.modules, ",") {
			t.Errorf("Resolve(%q).Modules = %v, want %v", tc.capability, res.Modules, tc.modules)
		}
	}
	// A capability the core owns still resolves from the core.
	res, err := eff.Resolve("system.tag.readBlocking")
	if err != nil || res.Layer != LayerCore {
		t.Errorf("core capability resolved to %q (%v), want core", res.Layer, err)
	}
	if got := eff.OverlayCounts(); got != (Counts{NativeFunctions: 1, CapabilityRules: 1, RestOperations: 1}) {
		t.Errorf("overlay counts = %+v, want one row per plane", got)
	}
	if got := eff.Counts(); got.BuiltinModules != eff.CoreCounts().BuiltinModules {
		t.Errorf("effective built-in modules = %d, want the core count", got.BuiltinModules)
	}
	if eff.OverlayDigest() == "" || !strings.HasPrefix(eff.OverlayDigest(), "sha256:") {
		t.Errorf("overlay digest = %q, want a sha256", eff.OverlayDigest())
	}
}

// An overlay row that has the same key as a core row is a fault naming both rows.
func TestOverlayConflictWithCore(t *testing.T) {
	root, declared := writeOverlay(t, `# plane: rest
GET	/data/api/v1/gateway-info	module	com.acme.hijack
`)
	overlay, err := LoadOverlay(root, []string{declared})
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	_, err = New(Target, overlay)
	if err == nil {
		t.Fatal("New accepted an overlay row that shadows a core row")
	}
	if err.Code != contract.CodeOverlayConflict {
		t.Errorf("code = %q, want %q", err.Code, contract.CodeOverlayConflict)
	}
	for _, want := range []string{"com.acme.hijack", "core", "GET /data/api/v1/gateway-info"} {
		if !strings.Contains(err.Message, want) {
			t.Errorf("message = %q, want it to name %q", err.Message, want)
		}
	}
}

// The same rule applies between two overlay files, and for the other planes.
func TestOverlayConflictForEveryPlane(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		paths []string
	}{
		{
			name: "native function shadows the core",
			files: map[string]string{
				"catalog/overlay.tsv": "# plane: native-function\nsystem.tag.readBlocking\tplatform\t-\t\n",
			},
			paths: []string{"catalog/overlay.tsv"},
		},
		{
			name: "capability rule repeated across overlay files",
			files: map[string]string{
				"catalog/one.tsv": "# plane: capability-rule\nexact\tacme.thing\tcom.acme\tfirst\n",
				"catalog/two.tsv": "# plane: capability-rule\nexact\tacme.thing\tcom.acme.other\tsecond\n",
			},
			paths: []string{"catalog/one.tsv", "catalog/two.tsv"},
		},
		{
			name: "rest repeated across overlay files",
			files: map[string]string{
				"catalog/one.tsv": "# plane: rest\nGET\t/data/acme/x\tmodule\tcom.acme\n",
				"catalog/two.tsv": "# plane: rest\nGET\t/data/acme/x/\tmodule\tcom.acme\n",
			},
			paths: []string{"catalog/one.tsv", "catalog/two.tsv"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for declared, body := range tc.files {
				full := filepath.Join(root, declared)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			overlay, err := LoadOverlay(root, tc.paths)
			if err != nil {
				t.Fatalf("LoadOverlay: %v", err)
			}
			if _, err := New(Target, overlay); err == nil || err.Code != contract.CodeOverlayConflict {
				t.Fatalf("New = %v, want IGDEV_E_OVERLAY_CONFLICT", err)
			}
		})
	}
}

// An overlay file that cannot be understood is the project's to fix, and the
// message says which file and line.
func TestOverlayFormatFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "no directive", body: "GET\t/data/acme/x\tmodule\tcom.acme\n"},
		{name: "unknown plane", body: "# plane: widget\nGET\t/data/acme/x\tmodule\tcom.acme\n"},
		{name: "bad classification", body: "# plane: native-function\nsystem.acme.x\tsideways\tcom.acme\t\n"},
		{name: "bad owner", body: "# plane: rest\nGET\t/data/acme/x\tsideways\tcom.acme\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, declared := writeOverlay(t, tc.body)
			_, err := LoadOverlay(root, []string{declared})
			if err == nil {
				t.Fatal("LoadOverlay accepted a malformed overlay")
			}
			if err.Code != contract.CodeOverlayInvalid {
				t.Errorf("code = %q, want %q", err.Code, contract.CodeOverlayInvalid)
			}
			if !strings.Contains(err.Message, "overlay") || !strings.Contains(err.Message, "line") {
				t.Errorf("message = %q, want the file and line named", err.Message)
			}
		})
	}
}

// A declared overlay file that does not exist is a fault: the contract lies.
func TestOverlayMissingFile(t *testing.T) {
	_, err := LoadOverlay(t.TempDir(), []string{"catalog/absent.tsv"})
	if err == nil || err.Code != contract.CodeOverlayInvalid {
		t.Fatalf("LoadOverlay = %v, want IGDEV_E_OVERLAY_INVALID", err)
	}
}

// An overlay path outside the Project Root is refused even if the contract
// validation is bypassed.
func TestOverlayPathEscapesRoot(t *testing.T) {
	root, _ := writeOverlay(t, "# plane: rest\n")
	for _, declared := range []string{"/etc/passwd", "../outside.tsv", ""} {
		if _, err := LoadOverlay(root, []string{declared}); err == nil {
			t.Errorf("LoadOverlay accepted %q", declared)
		}
	}
}

// Scanning reports every reference with its file and line, including nested
// namespaces, and a method reference supersedes the bare path it contains.
func TestScanReportsFileAndLine(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "src", "python")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	script := filepath.Join(nested, "handlers.py")
	body := strings.Join([]string{
		"# a comment naming system.not.a.real.function",
		"system.historian.types.dataPoint(1, 2, 3)",
		"system.tag.readBlocking([])",
		"response = client.get('/data/reporting/api/v1/reports/current')",
		"curl /data/perspective/api/v1/sessions/",
	}, "\n")
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	// A file with an extension outside the walk set is skipped in a directory.
	if err := os.WriteFile(filepath.Join(nested, "notes.txt"), []byte("system.tag.readBlocking"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	result := Scan([]string{dir})
	want := map[string]int{
		"system.historian.types.dataPoint":       2,
		"system.tag.readBlocking":                3,
		"/data/reporting/api/v1/reports/current": 4,
		"/data/perspective/api/v1/sessions/":     5,
		"system.not.a.real.function":             1,
	}
	got := map[string]int{}
	for _, finding := range result.Findings {
		got[finding.Capability] = finding.Line
		if !strings.HasSuffix(finding.File, "handlers.py") {
			t.Errorf("finding %+v came from an unexpected file", finding)
		}
	}
	for capability, line := range want {
		if got[capability] != line {
			t.Errorf("finding %q = line %d, want %d (all: %v)", capability, got[capability], line, got)
		}
	}
	if len(result.Missing) != 0 {
		t.Errorf("missing = %v, want none", result.Missing)
	}
}

// A method reference suppresses the bare path anywhere in the same file, because
// the method carries strictly more information. This is the bash specification's
// behavior: the bare-path list drops every path some method already named.
func TestScanMethodSupersedesBarePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calls.py")
	body := "requests.get('GET /data/api/v1/gateway-info')\nurl = '/data/api/v1/gateway-info'\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	result := Scan([]string{path})
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %+v, want the method call alone", result.Findings)
	}
	if result.Findings[0].Capability != "GET /data/api/v1/gateway-info" || result.Findings[0].Line != 1 {
		t.Errorf("finding = %+v, want the method call on line 1", result.Findings[0])
	}
}

// A missing scan path is reported, not fatal.
func TestScanMissingPath(t *testing.T) {
	result := Scan([]string{filepath.Join(t.TempDir(), "absent")})
	if len(result.Missing) != 1 {
		t.Errorf("missing = %v, want the absent path", result.Missing)
	}
	if len(result.Findings) != 0 {
		t.Errorf("findings = %v, want none", result.Findings)
	}
}

// The capability rules plane is consulted exact-then-prefix, and the overlay can
// add a rule the core does not have.
func TestRuleLookupOrder(t *testing.T) {
	root, declared := writeOverlay(t, `# plane: capability-rule
prefix	report.	com.inductiveautomation.reporting	any reporting alias
exact	report.execute	com.inductiveautomation.reporting.execute	an exact override of the prefix
`)
	eff := loadOverlay(t, root, []string{declared})
	res, err := eff.Resolve("report.execute")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Join(res.Modules, ",") != "com.inductiveautomation.reporting.execute" {
		t.Errorf("modules = %v, want the exact rule despite its later position", res.Modules)
	}
	if _, err := eff.Resolve("report.other"); err != nil {
		t.Errorf("Resolve(report.other) = %v, want the prefix rule", err)
	}
}

// The overlay digest is the layer's integrity: it covers the declared file
// bytes, so any edit to the overlay is visible in `catalog status`.
func TestOverlayDigestTracksFileBytes(t *testing.T) {
	root, declared := writeOverlay(t, "# plane: rest\nGET\t/data/acme/x\tmodule\tcom.acme\n")
	first := loadOverlay(t, root, []string{declared})
	second := loadOverlay(t, root, []string{declared})
	if first.OverlayDigest() != second.OverlayDigest() {
		t.Errorf("digest is not stable: %q then %q", first.OverlayDigest(), second.OverlayDigest())
	}

	// A comment-only edit still changes the digest: it covers bytes, and every
	// byte of a tracked file is reviewable.
	body := "# plane: rest\n# a new comment\nGET\t/data/acme/x\tmodule\tcom.acme\n"
	if err := os.WriteFile(filepath.Join(root, declared), []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	third := loadOverlay(t, root, []string{declared})
	if third.OverlayDigest() == first.OverlayDigest() {
		t.Errorf("digest did not change with the file")
	}
	if third.CoreDigest() != first.CoreDigest() {
		t.Errorf("core digest changed with an overlay edit")
	}
}

// An empty overlay list still yields a layer with a digest, so `catalog status`
// always reports both layers.
func TestEmptyOverlayHasDigest(t *testing.T) {
	eff := loadOverlay(t, t.TempDir(), nil)
	if !strings.HasPrefix(eff.OverlayDigest(), "sha256:") {
		t.Errorf("empty overlay digest = %q, want a sha256", eff.OverlayDigest())
	}
	if got := eff.OverlayCounts(); got != (Counts{}) {
		t.Errorf("empty overlay counts = %+v, want zero", got)
	}
	if len(eff.OverlayPaths()) != 0 {
		t.Errorf("overlay paths = %v, want none", eff.OverlayPaths())
	}
}

// One concrete path matching templates with different owners cannot be resolved
// for the caller; the fault names the candidates.
func TestAmbiguousRESTOwnership(t *testing.T) {
	root, declared := writeOverlay(t, "# plane: rest\nGET\t/data/perspective/api/v1/{segment}\tmodule\tcom.acme.ambiguous\n")
	eff := loadOverlay(t, root, []string{declared})
	_, err := eff.Resolve("GET /data/perspective/api/v1/sessions")
	if err == nil {
		t.Fatal("Resolve accepted an ambiguous REST capability")
	}
	if err.Code != contract.CodeCapabilityAmbiguous {
		t.Errorf("code = %q, want %q", err.Code, contract.CodeCapabilityAmbiguous)
	}
	for _, want := range []string{"com.acme.ambiguous", "com.inductiveautomation.perspective"} {
		if !strings.Contains(err.Message, want) {
			t.Errorf("message = %q, want it to name %q", err.Message, want)
		}
	}
}

// REST request parsing: method case is irrelevant, a query string is dropped, and
// anything that is neither a method-and-path nor a /data path is not a request.
func TestParseRESTRequest(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  RESTRequest
		ok    bool
	}{
		{input: "get /data/api/v1/gateway-info", want: RESTRequest{Method: "GET", Path: "/data/api/v1/gateway-info"}, ok: true},
		{input: "PATCH /data/api/v1/thing", want: RESTRequest{Method: "PATCH", Path: "/data/api/v1/thing"}, ok: true},
		{input: "/data/api/v1/gateway-info", want: RESTRequest{Path: "/data/api/v1/gateway-info"}, ok: true},
		{input: "/data/perspective/api/v1/sessions/", want: RESTRequest{Path: "/data/perspective/api/v1/sessions/"}, ok: true},
		{input: "GET /data/api/v1/thing?filter=x", want: RESTRequest{Method: "GET", Path: "/data/api/v1/thing"}, ok: true},
		{input: "FOO /data/api/v1/thing"},
		{input: "GET /other/path"},
		{input: "GET"},
		{input: ""},
		{input: "system.tag.readBlocking"},
	} {
		got, ok := ParseRESTRequest(tc.input)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRESTRequest(%q) = (%+v, %v), want (%+v, %v)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

// A bare path matches every method, and several templates can match it. The
// reported template is the most specific match with the earliest method, so the
// answer is stable rather than an accident of catalog order.
func TestRESTSpecificityTieBreak(t *testing.T) {
	eff := defaultEffective(t)
	res, err := eff.Resolve("/data/perspective/api/v1/sessions/")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Method != "" {
		t.Errorf("method = %q, want empty for a bare-path request", res.Method)
	}
	if res.Template != "/data/perspective/api/v1/sessions/" {
		t.Errorf("template = %q, want the GET template", res.Template)
	}
	if res.Platform {
		t.Errorf("platform = true, want the perspective module")
	}
	if strings.Join(res.Modules, ",") != "com.inductiveautomation.perspective" {
		t.Errorf("modules = %v, want the perspective module", res.Modules)
	}

	// A literal template beats a "{param}" one for the same concrete path, even
	// when the overlay's row came later: the more specific template decides.
	root := mustOverlay(t, "# plane: rest\nGET\t/data/perspective/api/v1/{segment}\tmodule\tcom.inductiveautomation.perspective\n")
	overlay, fault := LoadOverlay(root, []string{"catalog/overlay.tsv"})
	if fault != nil {
		t.Fatalf("LoadOverlay: %v", fault)
	}
	withOverlay, fault := New(Target, overlay)
	if fault != nil {
		t.Fatalf("New: %v", fault)
	}
	res, err = withOverlay.Resolve("GET /data/perspective/api/v1/sessions")
	if err != nil {
		t.Fatalf("Resolve with overlay: %v", err)
	}
	if res.Template != "/data/perspective/api/v1/sessions/" || res.Layer != LayerCore {
		t.Errorf("template = %q (layer %q), want the core literal template", res.Template, res.Layer)
	}
}

// mustOverlay writes one overlay file in a scratch root and returns its root.
func mustOverlay(t *testing.T, body string) string {
	t.Helper()
	root, _ := writeOverlay(t, body)
	return root
}
