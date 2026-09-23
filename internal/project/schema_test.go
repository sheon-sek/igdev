package project

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// The contract `igdev init` writes must parse back to exactly the document it
// was rendered from: the printer and the parser are one round trip, so a second
// init run can never look like a change.
func TestRenderRoundTrips(t *testing.T) {
	doc := DefaultDoc()
	doc.Project.Name = "repo"
	doc.Tool.MinVersion = "0.1.0"
	doc.Modules.Enabled = []string{"com.inductiveautomation.perspective", "com.inductiveautomation.opcua"}
	doc.Scan.Jython = []string{"src/main/python", "ignition/script-python"}
	doc.Catalog.OverlayPaths = []string{"catalog/overlay.tsv", "catalog/acme.tsv"}
	doc.Commands.Check = "./gradlew check"
	doc.Commands.Smoke = "curl -fsS http://localhost/"
	doc.Gateway.MemoryMB = 4096
	doc.Gateway.Timezone = "Asia/Kuala_Lumpur"

	rendered := doc.Render()
	parsed, err := ParseDoc(rendered, "igdev.toml")
	if err != nil {
		t.Fatalf("ParseDoc(render) = %v\n%s", err, rendered)
	}
	if !equalDocs(doc, parsed) {
		t.Errorf("round trip changed the document:\nrendered:\n%s\nparsed: %+v", rendered, parsed)
	}
	if again := parsed.Render(); string(again) != string(rendered) {
		t.Errorf("render is not idempotent:\nfirst:\n%s\nsecond:\n%s", rendered, again)
	}
}

// [gateway] allow_unsigned_modules is additive to schema v1: a contract that does
// not state it reports the default, renders byte-identically to a contract written
// before the key existed (no key at all), and survives a round trip when stated.
func TestAllowUnsignedModulesIsAdditive(t *testing.T) {
	absent, err := ParseDoc([]byte("schema = 1\n\n[gateway]\nmemory_mb = 2048\ntimezone = \"UTC\"\n"), "igdev.toml")
	if err != nil {
		t.Fatalf("ParseDoc without the key: %v", err)
	}
	if absent.Gateway.AllowUnsignedModules {
		t.Error("a contract that does not state the key reports the Gateway loading unsigned modules")
	}
	if rendered := string(absent.Render()); strings.Contains(rendered, "allow_unsigned_modules") {
		t.Errorf("render emitted the key at its default:\n%s", rendered)
	}

	stated, err := ParseDoc([]byte("schema = 1\n\n[gateway]\nmemory_mb = 2048\ntimezone = \"UTC\"\nallow_unsigned_modules = true\n"), "igdev.toml")
	if err != nil {
		t.Fatalf("ParseDoc with the key: %v", err)
	}
	if !stated.Gateway.AllowUnsignedModules {
		t.Error("the stated true did not parse")
	}
	rendered := string(stated.Render())
	if !strings.Contains(rendered, "allow_unsigned_modules = true") {
		t.Errorf("render dropped the stated key:\n%s", rendered)
	}
	if !strings.Contains(string(stated.Filled(DefaultDoc()).Render()), "allow_unsigned_modules = true") {
		t.Error("Filled cleared the stated key")
	}
	if again, err := ParseDoc([]byte(rendered), "igdev.toml"); err != nil || !again.Gateway.AllowUnsignedModules {
		t.Errorf("round trip lost the key: %v", err)
	}
}

// A key igdev does not know is refused, not ignored: schema v1 is a closed
// layout, and a typo must not silently do nothing.
func TestParseDocRejectsUnknownKeys(t *testing.T) {
	_, err := ParseDoc([]byte("schema = 1\n\n[ignition]\nversion = \"8.3.8\"\njython_verison = \"2.7.4\"\n"), "igdev.toml")
	if err == nil {
		t.Fatal("an unknown key parsed")
	}
	fault := contract.AsFault(err)
	if fault.Code != contract.CodeConfigInvalid {
		t.Errorf("code = %s, want %s", fault.Code, contract.CodeConfigInvalid)
	}
	if !strings.Contains(fault.Message, "ignition.jython_verison") {
		t.Errorf("fault does not name the offending key: %q", fault.Message)
	}
	if len(fault.Remediation) == 0 || fault.Remediation[0].Command != "igdev init" {
		t.Errorf("fault carries no repair path: %+v", fault.Remediation)
	}
}

// Values are checked, not merely read: a version field holds a version.
func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*Doc)
		want  string
	}{
		{"ignition version", func(d *Doc) { d.Ignition.Version = "latest" }, "ignition.version"},
		{"jython version", func(d *Doc) { d.Ignition.JythonVersion = "two" }, "ignition.jython_version"},
		{"edition", func(d *Doc) { d.Ignition.Edition = "Standard Edition" }, "ignition.edition"},
		{"tool min version", func(d *Doc) { d.Tool.MinVersion = "soon" }, "tool.min_version"},
		{"module id", func(d *Doc) { d.Modules.Enabled = []string{"has space"} }, "modules.enabled"},
		{"empty scan path", func(d *Doc) { d.Scan.Jython = []string{" "} }, "scan.jython"},
		{"gateway heap", func(d *Doc) { d.Gateway.MemoryMB = -1 }, "gateway.memory_mb"},
		{"empty overlay path", func(d *Doc) { d.Catalog.OverlayPaths = []string{" "} }, "catalog.overlay_paths"},
		{"absolute overlay path", func(d *Doc) { d.Catalog.OverlayPaths = []string{"/etc/catalog.tsv"} }, "catalog.overlay_paths"},
		{"traversing overlay path", func(d *Doc) { d.Catalog.OverlayPaths = []string{"../catalog.tsv"} }, "catalog.overlay_paths"},
		{"timezone", func(d *Doc) { d.Gateway.Timezone = "Mars Olympus" }, "gateway.timezone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := DefaultDoc()
			tc.apply(&doc)
			err := doc.Validate("igdev.toml")
			if err == nil {
				t.Fatalf("%s accepted", tc.name)
			}
			fault := contract.AsFault(err)
			if fault.Code != contract.CodeConfigInvalid {
				t.Errorf("code = %s, want %s", fault.Code, contract.CodeConfigInvalid)
			}
			if !strings.Contains(fault.Message, tc.want) {
				t.Errorf("message %q does not name %q", fault.Message, tc.want)
			}
		})
	}
}

// A contract that states nothing but its schema is valid: every unset field is
// covered by the embedded default, so a minimal file is accepted rather than
// forced to spell out values igdev already knows.
func TestValidateAcceptsUnsetFields(t *testing.T) {
	doc, err := DecodeDoc([]byte("schema = 1\n\n[project]\nname = \"fixture\"\n"))
	if err != nil {
		t.Fatalf("DecodeDoc: %v", err)
	}
	if err := doc.Validate("igdev.toml"); err != nil {
		t.Errorf("a minimal contract was refused: %v", err)
	}
}

// Filled carries a hand-written file forward: what it states wins, what it omits
// falls back to the defaults, so a partial contract is still writable.
func TestFilledCompletesPartialContract(t *testing.T) {
	existing, err := DecodeDoc([]byte("schema = 1\n\n[ignition]\nversion = \"8.1.21\"\n\n[commands]\ncheck = \"./check.sh\"\n"))
	if err != nil {
		t.Fatalf("DecodeDoc: %v", err)
	}
	doc := existing.Filled(DefaultDoc())
	if doc.Ignition.Version != "8.1.21" {
		t.Errorf("ignition.version = %q, want the file's value", doc.Ignition.Version)
	}
	if doc.Ignition.JythonVersion != DefaultJythonVersion {
		t.Errorf("jython_version = %q, want the default backfilled", doc.Ignition.JythonVersion)
	}
	if doc.Gateway.MemoryMB != DefaultGatewayMemoryMB {
		t.Errorf("memory_mb = %d, want the default backfilled", doc.Gateway.MemoryMB)
	}
	if doc.Commands.Check != "./check.sh" {
		t.Errorf("commands.check = %q, want the file's value", doc.Commands.Check)
	}
	if err := doc.Validate("igdev.toml"); err != nil {
		t.Errorf("a completed partial contract does not validate: %v", err)
	}
}

// The digest names its algorithm and moves with any byte of the contract.
func TestDigestCoversBytes(t *testing.T) {
	a := Digest([]byte("schema = 1\n"))
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Fatalf("digest %q is not a named sha256", a)
	}
	if a == Digest([]byte("schema = 1\n\n")) {
		t.Error("digest did not change when the bytes did")
	}
}

// An empty modules list and an explicit empty array mean the same thing: nothing
// enabled. The distinction matters because a hand-written `enabled = []` must not
// be read as "unset" and replaced by a default.
func TestDecodeEmptyLists(t *testing.T) {
	doc, err := DecodeDoc([]byte("schema = 1\n\n[modules]\nenabled = []\n"))
	if err != nil {
		t.Fatalf("DecodeDoc: %v", err)
	}
	if doc.Modules.Enabled == nil {
		t.Error("an explicit enabled = [] decoded as nil (unset)")
	}
	if len(doc.Modules.Enabled) != 0 {
		t.Errorf("enabled = %v, want empty", doc.Modules.Enabled)
	}
}

func equalDocs(a, b Doc) bool {
	return string(a.Render()) == string(b.Render())
}

// A contract that does not state the catalog section declares no overlay and
// behaves exactly as before: the key is additive to schema v1, so existing files
// keep parsing untouched.
func TestCatalogSectionIsAdditive(t *testing.T) {
	doc, err := DecodeDoc([]byte("schema = 1\n\n[project]\nname = \"fixture\"\n"))
	if err != nil {
		t.Fatalf("DecodeDoc: %v", err)
	}
	if !doc.Catalog.Empty() {
		t.Errorf("catalog = %+v, want an empty section when the key is absent", doc.Catalog)
	}
	if strings.Contains(string(doc.Render()), "[catalog]") {
		t.Errorf("render emitted an empty [catalog] section:\n%s", doc.Render())
	}

	withOverlay, err := ParseDoc([]byte("schema = 1\n\n[catalog]\noverlay_paths = [\"catalog/overlay.tsv\"]\n"), "igdev.toml")
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if got := strings.Join(withOverlay.Catalog.OverlayPaths, ","); got != "catalog/overlay.tsv" {
		t.Errorf("overlay_paths = %q, want the declared path", got)
	}
	if !strings.Contains(string(withOverlay.Render()), "[catalog]") {
		t.Errorf("render dropped the declared overlay:\n%s", withOverlay.Render())
	}
	// Filling a document that omits the key must not invent one.
	filled := withOverlay.Filled(DefaultDoc())
	if got := strings.Join(filled.Catalog.OverlayPaths, ","); got != "catalog/overlay.tsv" {
		t.Errorf("Filled changed overlay_paths to %q", got)
	}
}
