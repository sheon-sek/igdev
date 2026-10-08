package lookup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bundle in the shape the 8.3.8 jars carry, continuation lines included.
const tagBundle = `# Tag functions
readBlocking.desc=Reads the value of the tags at the given tag paths. \
  This function will block until the read operation is complete.
readBlocking.param.tagPaths=A list of tag paths to read from.
readBlocking.param.timeout=How long to wait (in milliseconds) before the read operation times out.
readBlocking.param.timeout.default=45000
readBlocking.returns=A list of qualified values.
readAsync.desc=Asynchronously reads the value of the tags at the given tag paths.
readAsync.param.tagPaths=A list of tag paths to read from.
writeBlocking.desc=Writes values to tags at the given paths.
queryTagHistory.deprecated=This function is deprecated.
queryTagHistory.replacement=system.historian.queryRawPoints
queryTagHistory.desc=Issues a query to the Tag Historian.
`

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPropertiesJoinContinuationsAndKeepOrder(t *testing.T) {
	p := parseProperties([]byte(tagBundle + "escaped=a\\u0041\\tb\nspaced key = v\n"))
	if got := p.values["readBlocking.desc"]; got != "Reads the value of the tags at the given tag paths. This function will block until the read operation is complete." {
		t.Errorf("continuation = %q", got)
	}
	if p.values["escaped"] != "aA\tb" || p.values["spaced"] != "key = v" {
		t.Errorf("escapes = %q, %q", p.values["escaped"], p.values["spaced"])
	}
	if p.keys[0] != "readBlocking.desc" || p.keys[1] != "readBlocking.param.tagPaths" {
		t.Errorf("key order = %v", p.keys[:2])
	}
}

// The function index is read from a jar's bundles, placed by the namespace
// table, with the scopes of the directory the jar sits in.
func TestImageBundlesBecomeFunctions(t *testing.T) {
	jar := zipOf(t, map[string][]byte{
		"com/inductiveautomation/ignition/common/script/builtin/AbstractTagUtilities.properties":    []byte(tagBundle),
		"com/inductiveautomation/ignition/common/script/builtin/AbstractTagUtilities_de.properties": []byte("readBlocking.desc=Liest.\n"),
		"com/example/NotABundle.properties": []byte("title=nothing here\n"),
	})
	gatewayJar := zipOf(t, map[string][]byte{
		"com/inductiveautomation/ignition/gateway/script/GatewaySystemUtilities.properties": []byte("getModules.desc=Lists the modules.\n"),
	})
	core := tarOf(t, map[string][]byte{"core/common/common.jar": jar, "core/gateway/gateway.jar": gatewayJar, "core/launch/x.jar": jar})
	bundles, err := readCoreTar(bytes.NewReader(core), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fns := buildFunctions(bundles, []string{"system.tag.readBlocking", "system.util.getModules"}, false)
	byName := map[string]Function{}
	for _, f := range fns {
		byName[f.Name] = f
	}
	rb, ok := byName["system.tag.readBlocking"]
	if !ok {
		t.Fatalf("system.tag.readBlocking is missing from %v", names(fns))
	}
	if ScopeLabel(rb.Scopes) != "all" || rb.Module != Platform || len(rb.Params) != 2 || rb.Params[1].Default != "45000" || rb.Returns == "" {
		t.Errorf("readBlocking = %+v", rb)
	}
	if !strings.HasSuffix(rb.Source, "AbstractTagUtilities.properties") || !strings.HasPrefix(rb.Source, "lib/core/common/common.jar!/") {
		t.Errorf("source = %q", rb.Source)
	}
	if got := byName["system.util.getModules"]; ScopeLabel(got.Scopes) != "gateway" {
		t.Errorf("getModules scopes = %v", got.Scopes)
	}
	if byName["system.tag.queryTagHistory"].Replacement != "system.historian.queryRawPoints" {
		t.Errorf("deprecation was not read: %+v", byName["system.tag.queryTagHistory"])
	}
	if len(fns) != 5 {
		t.Errorf("functions = %v, want the four tag functions and getModules", names(fns))
	}
}

// A built-in module archive's bundles get the module id and the scopes
// module.xml gives the jar; a function the bundle mixes in from another
// namespace is placed where the catalog has it.
func TestModuleBundlesUseModuleXML(t *testing.T) {
	jar := zipOf(t, map[string][]byte{
		"com/inductiveautomation/ignition/alarming/common/scripting/AbstractAlarmScriptModule.properties": []byte(
			"getRoster.desc=Gets a roster.\ngetRosterNames.desc=Lists roster names.\nlistPipelines.desc=Lists pipelines.\n" +
				"createRoster.desc=Creates a roster.\ngetRosters.desc=Lists rosters.\n"),
	})
	modl := zipOf(t, map[string][]byte{
		"module.xml": []byte(`<?xml version="1.0"?><modules><module><id>com.inductiveautomation.alarm-notification</id>` +
			`<jar scope="CDG">alarm-common.jar</jar></module></modules>`),
		"alarm-common.jar": jar,
	})
	bundles, err := readModulesTar(bytes.NewReader(tarOf(t, map[string][]byte{"modules/Alarm Notification-module.modl": modl})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	known := []string{"system.roster.getRoster", "system.roster.getRosterNames", "system.alarm.listPipelines",
		"system.alarm.createRoster", "system.alarm.getRosters", "system.roster.createRoster", "system.roster.getRosters"}
	got := names(buildFunctions(bundles, known, false))
	want := "system.alarm.createRoster system.alarm.getRosters system.alarm.listPipelines system.roster.createRoster system.roster.getRoster system.roster.getRosterNames system.roster.getRosters"
	if got != want {
		t.Errorf("functions = %s\nwant        %s", got, want)
	}
	if bundles[0].Module != "com.inductiveautomation.alarm-notification" || ScopeLabel(bundles[0].Scopes) != "all" {
		t.Errorf("bundle = %s %v", bundles[0].Module, bundles[0].Scopes)
	}
}

// A private module's bundle no table or catalog row places is kept under its
// class name, with a note; a platform bundle like that is left out.
func TestPrivateModuleKeepsAnUnmappedBundle(t *testing.T) {
	jar := zipOf(t, map[string][]byte{"com/acme/AcmeScripts.properties": []byte("frobnicate.desc=Frobnicates.\nfrobnicate.param.level=How hard.\n")})
	modl := zipOf(t, map[string][]byte{
		"module.xml":    []byte(`<modules><module><id>com.acme.frob</id><jar scope="G">acme.jar</jar></module></modules>`),
		"acme.jar":      jar,
		"unlisted.jar":  jar,
		"doc/readme.md": []byte("x"),
	})
	path := filepath.Join(t.TempDir(), "acme.modl")
	if err := os.WriteFile(path, modl, 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := BuildModuleIndex(path, "abc", "abc-key", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Functions) != 1 || idx.Functions[0].Name != "AcmeScripts.frobnicate" || idx.Functions[0].Module != "com.acme.frob" ||
		idx.Functions[0].Note == "" || ScopeLabel(idx.Functions[0].Scopes) != "gateway" {
		t.Errorf("private index = %+v", idx.Functions)
	}
	bundles, _ := readModuleFile(path, t.TempDir())
	if fns := buildFunctions(bundles, nil, false); len(fns) != 0 {
		t.Errorf("an unmapped platform bundle was kept: %v", names(fns))
	}
}

const openAPIFixture = `{
  "openapi": "3.1.0",
  "paths": {
    "/data/api/v1/projects/import/{name}": {
      "parameters": [{"$ref": "#/components/parameters/Name"}],
      "post": {
        "summary": "Import a project into this Ignition Gateway.",
        "description": "Import a project into this Ignition Gateway.",
        "tags": ["Projects"],
        "parameters": [{"name": "overwrite", "in": "query", "description": "Replace an existing project."}],
        "requestBody": {"content": {"application/zip": {"schema": {"type": "string", "format": "binary"}}}},
        "responses": {"200": {"description": "OK", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Result"}}}}}
      }
    },
    "/data/api/v1/projects": {
      "get": {"summary": "List the projects.", "tags": ["Projects"], "responses": {"200": {"description": "OK"}}},
      "post": {"summary": "Create a project.", "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}}}}
    },
    "/data/api/v1/tags/import": {
      "post": {"summary": "Import tags from a file.", "tags": ["Tags"]}
    }
  },
  "components": {
    "parameters": {"Name": {"name": "name", "in": "path", "required": true, "description": "The project name."}},
    "schemas": {
      "Result": {"type": "object", "properties": {"ok": {"type": "boolean"}}},
      "Node": {"type": "object", "properties": {"child": {"$ref": "#/components/schemas/Node"}}}
    }
  }
}`

func TestOpenAPIIndexAndDetail(t *testing.T) {
	eps, err := ParseOpenAPI([]byte(openAPIFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 4 {
		t.Fatalf("endpoints = %d, want 4", len(eps))
	}
	var imp Endpoint
	for _, e := range eps {
		if e.Name() == "POST /data/api/v1/projects/import/{name}" {
			imp = e
		}
	}
	if imp.Summary == "" || len(imp.Params) != 2 || imp.Params[1].Name != "name" || !imp.Params[1].Required ||
		len(imp.RequestTypes) != 1 || imp.RequestTypes[0] != "application/zip" || imp.Source != SourceOpenAPI {
		t.Errorf("import endpoint = %+v", imp)
	}
	detail, ok := Detail([]byte(openAPIFixture), "POST", "/data/api/v1/projects/import/{name}")
	if !ok {
		t.Fatal("no detail")
	}
	ok200 := detail.Responses["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)
	if ok200["type"] != "object" {
		t.Errorf("the response $ref was not inlined: %v", ok200)
	}
	// A schema that refers to itself is inlined to a bound, not forever.
	if _, ok := Detail([]byte(openAPIFixture), "POST", "/data/api/v1/projects"); !ok {
		t.Error("no detail for the recursive schema")
	}
	if _, err := ParseOpenAPI([]byte(`{"openapi":"3.1.0"}`)); err == nil {
		t.Error("a document without paths was accepted")
	}
}

func TestSearchRanksNamesAboveDescriptions(t *testing.T) {
	jar := zipOf(t, map[string][]byte{"x/AbstractTagUtilities.properties": []byte(tagBundle)})
	bundles, err := readCoreTar(bytes.NewReader(tarOf(t, map[string][]byte{"core/common/c.jar": jar})), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fns := buildFunctions(bundles, nil, false)
	eps, _ := ParseOpenAPI([]byte(openAPIFixture))

	hits := Search(fns, eps, "read tag values", "", 3)
	if len(hits) == 0 || (hits[0].Name != "system.tag.readBlocking" && hits[1].Name != "system.tag.readBlocking") {
		t.Errorf("read tag values = %v", hitNames(hits))
	}
	hits = Search(fns, eps, "import project", "", 3)
	if len(hits) == 0 || hits[0].Name != "POST /data/api/v1/projects/import/{name}" {
		t.Errorf("import project = %v", hitNames(hits))
	}
	if hits := Search(fns, eps, "import project", KindFunction, 3); len(hits) != 0 {
		t.Errorf("--kind function returned %v", hitNames(hits))
	}
	if hits := Search(fns, eps, "the of", "", 3); hits != nil {
		t.Errorf("a query of stop words matched %v", hitNames(hits))
	}
}

func TestTermsSplitCamelCaseAndStem(t *testing.T) {
	got := strings.Join(terms("readBlocking toCSV getHTTPClient values /data/api/v1/projects/{name} reading"), " ")
	if got != "read block csv get http client valu project name read" {
		t.Errorf("terms = %q", got)
	}
}

func names(fns []Function) string {
	var out []string
	for _, f := range fns {
		out = append(out, f.Name)
	}
	return strings.Join(out, " ")
}

func hitNames(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Name)
	}
	return out
}
