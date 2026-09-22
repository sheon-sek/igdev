package catalog

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// The classification table is the ported mapping rule, and these are the anchors
// it pins: a route prefix, a resource module, each alias, the private module that
// never ships in the image, and the platform default. The row-level behaviour is
// exercised through the binary in itest; this test names the rule itself.
func TestClassifyOpenAPIPath(t *testing.T) {
	for _, tc := range []struct {
		path    string
		owner   OwnerKind
		modules string
	}{
		{path: "/data/api/v1/gateway-info", owner: OwnerPlatform},
		{path: "/data/api/v1/system/status", owner: OwnerPlatform},
		{path: "/data/reporting/api/v1/reports/current", owner: OwnerModule, modules: "com.inductiveautomation.reporting"},
		{path: "/data/reporting/api/v1/reports/{reportPath}", owner: OwnerModule, modules: "com.inductiveautomation.reporting"},
		{path: "/data/perspective/api/v1/sessions/", owner: OwnerModule, modules: "com.inductiveautomation.perspective"},
		// A route prefix only claims its own ancestry: /data/mcp without the
		// trailing slash is not the MCP module's.
		{path: "/data/mcp", owner: OwnerPlatform},
		{path: "/data/mcp/api/v1/tools/{toolName}", owner: OwnerPrivateModule, modules: "com.inductiveautomation.mcp"},
		{path: "/data/api/v1/resources/names/com.inductiveautomation.opcua/device", owner: OwnerModule, modules: "com.inductiveautomation.opcua"},
		// The module segment is the first com.* segment, not the first segment.
		{path: "/data/api/v1/resources/list/com.inductiveautomation.eam/eam-tasks", owner: OwnerModule, modules: "com.inductiveautomation.eam"},
		{path: "/data/api/v1/resources/com.inductiveautomation.mcp/servers", owner: OwnerPrivateModule, modules: "com.inductiveautomation.mcp"},
		{path: "/data/api/v1/resources/com.inductiveautomation.opcua.drivers.bacnet/IpDeviceConfig",
			owner: OwnerModule, modules: "com.inductiveautomation.opcua.drivers.bacnet,com.inductiveautomation.opcua"},
		{path: "/data/api/v1/resources/com.inductiveautomation.sip-notification/script-settings",
			owner: OwnerModule, modules: "com.inductiveautomation.phone-notification"},
		{path: "/data/api/v1/resources/no-module-here/widget", owner: OwnerPlatform},
	} {
		got := classifyOpenAPIPath(tc.path)
		if got.Owner != tc.owner {
			t.Errorf("%s owner = %q, want %q", tc.path, got.Owner, tc.owner)
		}
		if joined := strings.Join(got.Modules, ","); joined != tc.modules {
			t.Errorf("%s modules = %q, want %q", tc.path, joined, tc.modules)
		}
	}
}

// The parser is the generator's leniency: a path item that is not an object
// declares nothing, a member that is not one of the eight methods declares
// nothing, and the rows come out in path-then-method order.
func TestParseOpenAPIShapes(t *testing.T) {
	document := []byte(`{
	  "openapi": "3.0.1",
	  "paths": {
	    "/data/b": {"get": {}, "summary": "not an operation"},
	    "/data/a": {"POST": {}, "parameters": [], "delete": {}},
	    "/data/broken": "not an object"
	  }
	}`)
	operations, fault := ParseOpenAPI(document, "openapi.json")
	if fault != nil {
		t.Fatalf("ParseOpenAPI: %v", fault)
	}
	var got []string
	for _, op := range operations {
		got = append(got, op.Method+" "+op.Path)
	}
	want := []string{"DELETE /data/a", "GET /data/b"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("operations = %v, want %v", got, want)
	}

	for _, tc := range []struct {
		name     string
		document string
		message  string
	}{
		{name: "broken json", document: `{"paths":`, message: "is not valid JSON"},
		{name: "no paths", document: `{"openapi": "3.0.1"}`, message: "object-valued `paths` member"},
		{name: "paths not an object", document: `{"paths": []}`, message: "object-valued `paths` member"},
		{name: "no operations", document: `{"paths": {"/data/a": {}}}`, message: "declares no REST operations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, fault := ParseOpenAPI([]byte(tc.document), "openapi.json")
			if fault == nil {
				t.Fatalf("ParseOpenAPI accepted %s", tc.document)
			}
			if fault.Code != contract.CodeUsage || fault.Exit != contract.ExitUsage {
				t.Errorf("fault = %s/%d, want a usage error", fault.Code, fault.Exit)
			}
			if !strings.Contains(fault.Message, tc.message) || !strings.Contains(fault.Message, "openapi.json") {
				t.Errorf("message = %q, want it to name the document and say %q", fault.Message, tc.message)
			}
		})
	}
}

// Importing keeps the rows a previous import wrote that this document no longer
// declares, unless prune asks for them to go, and never copies a row the Core
// Catalog already carries with the same owner.
func TestImportOpenAPIMergesWithThePreviousBlock(t *testing.T) {
	first, fault := ImportOpenAPI(Target, nil, []OpenAPIOperation{
		{Method: "GET", Path: "/data/api/v1/system/status", Owner: OwnerPlatform},
		{Method: "POST", Path: "/data/mcp/api/v1/tools/{toolName}", Owner: OwnerPrivateModule, Modules: []string{"com.inductiveautomation.mcp"}},
	}, "sha256:aa", false, "catalog/rest-overlay.tsv")
	if fault != nil {
		t.Fatalf("first import: %v", fault)
	}
	if first.Added != 2 || first.Written != 2 || first.Preserved != 0 {
		t.Errorf("first import = %+v, want two added rows", first)
	}

	// The same document again is byte-identical: importing is idempotent.
	again, fault := ImportOpenAPI(Target, first.Content, []OpenAPIOperation{
		{Method: "GET", Path: "/data/api/v1/system/status", Owner: OwnerPlatform},
		{Method: "POST", Path: "/data/mcp/api/v1/tools/{toolName}", Owner: OwnerPrivateModule, Modules: []string{"com.inductiveautomation.mcp"}},
	}, "sha256:aa", false, "catalog/rest-overlay.tsv")
	if fault != nil {
		t.Fatalf("second import: %v", fault)
	}
	if string(again.Content) != string(first.Content) {
		t.Errorf("the same document produced different bytes:\n%s\n---\n%s", first.Content, again.Content)
	}
	if again.Added != 0 || again.Preserved != 0 || again.Removed != 0 {
		t.Errorf("second import = %+v, want no change", again)
	}

	// An operation the Core Catalog already carries identically adds nothing.
	redundant, fault := ImportOpenAPI(Target, first.Content, []OpenAPIOperation{
		{Method: "GET", Path: "/data/api/v1/system/status", Owner: OwnerPlatform},
		{Method: "GET", Path: "/data/api/v1/gateway-info", Owner: OwnerPlatform},
	}, "sha256:bb", false, "catalog/rest-overlay.tsv")
	if fault != nil {
		t.Fatalf("redundant import: %v", fault)
	}
	if redundant.Redundant != 1 || redundant.Preserved != 1 {
		t.Errorf("import = %+v, want one redundant operation and one preserved row", redundant)
	}
	if strings.Contains(string(redundant.Content), "/data/api/v1/gateway-info") {
		t.Errorf("the overlays repeats a core row:\n%s", redundant.Content)
	}

	// An operation the Core Catalog carries with another owner is a conflict.
	if _, fault := ImportOpenAPI(Target, nil, []OpenAPIOperation{
		{Method: "GET", Path: "/data/api/v1/gateway-info", Owner: OwnerModule, Modules: []string{"com.acme.widgets"}},
	}, "sha256:cc", false, "catalog/rest-overlay.tsv"); fault == nil || fault.Code != contract.CodeOverlayConflict {
		t.Errorf("conflicting import fault = %v, want IGDEV_E_OVERLAY_CONFLICT", fault)
	}
}
