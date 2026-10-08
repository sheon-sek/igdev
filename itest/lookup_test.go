package itest

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// lookupBundle is a trimmed copy of the 8.3.8 tag bundle.
const lookupBundle = `readBlocking.desc=Reads the value of the tags at the given tag paths. \
  This function will block until the read operation is complete or times out.
readBlocking.param.tagPaths=A list of tag paths to read from.
readBlocking.param.timeout=How long to wait (in milliseconds) before the read operation times out.
readBlocking.param.timeout.default=45000
readBlocking.returns=A list of qualified values corresponding to the tag paths.
readAsync.desc=Asynchronously reads the value of the tags at the given tag paths.
readAsync.param.tagPaths=A list of tag paths to read from.
readAsync.param.callback=A Python callback function to process the read results.
writeBlocking.desc=Writes values to tags at the given paths.
writeBlocking.param.tagPaths=The paths of the tags to write to.
writeBlocking.param.values=The values to write.
`

// lookupOpenAPI is a trimmed Gateway OpenAPI document.
const lookupOpenAPI = `{"openapi":"3.1.0","paths":{
 "/data/api/v1/projects/import/{name}":{"post":{"summary":"Import Project","description":"Import a project into this Ignition Gateway.",
  "tags":["projects"],"parameters":[{"name":"name","in":"path","required":true,"description":"The name of the Ignition Project."},
  {"name":"overwrite","in":"query","description":"Set true to overwrite an existing project of the same name."}],
  "requestBody":{"content":{"application/zip":{}}},
  "responses":{"200":{"description":"OK","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Changes"}}}}}}},
 "/data/api/v1/projects":{"post":{"summary":"Create Project","description":"Create a new project.","tags":["projects"],
  "requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}}}},
 "/data/api/v1/tags/import":{"post":{"summary":"Import Tags","description":"Import tags from a file.","tags":["tags"]}}},
 "components":{"schemas":{"Changes":{"type":"object","properties":{"changes":{"type":"array"}}}}}}`

// lookupImage stands in for the Ignition image: a lib/core tree with one
// common jar the docker shim streams back for `docker cp`.
func lookupImage(t *testing.T, env *testrig.Env) {
	t.Helper()
	root := env.Mkdir("image")
	install := filepath.Join(root, "usr", "local", "bin", "ignition")
	var jar bytes.Buffer
	w := zip.NewWriter(&jar)
	f, err := w.Create("com/inductiveautomation/ignition/common/script/builtin/AbstractTagUtilities.properties")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(lookupBundle)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(install, "lib", "core", "common"), filepath.Join(install, "user-lib", "modules")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(install, "lib", "core", "common", "common.jar"), jar.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	env.SetBaseEnv("IGDEV_SHIM_DOCKER_CP_ROOT=" + root)
}

type lookupResult struct {
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	Score         float64 `json:"score"`
	Module        string  `json:"module"`
	ModuleEnabled *bool   `json:"module_enabled"`
	Scope         string  `json:"scope"`
	Call          string  `json:"call"`
	Source        string  `json:"source"`
}

type lookupReport struct {
	Indexes struct {
		REST struct {
			Source    string `json:"source"`
			Endpoints int    `json:"endpoints"`
		} `json:"rest"`
	} `json:"indexes"`
	Results []lookupResult `json:"results"`
}

// Outside a Project Root, lookup reads the function index out of the image and
// falls back to the embedded REST plane; it leaves module_enabled out, and the
// container it opened is gone afterwards.
func TestLookupOutsideAProjectUsesTheImageAndTheEmbeddedPlane(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	lookupImage(t, env)
	dir := env.Mkdir("elsewhere")

	res := env.RunIn(dir, "lookup", "read tag values", "--limit", "3", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var report lookupReport
	testrig.DataOf(t, res.Stdout, &report)
	if report.Indexes.REST.Source != "embedded" || report.Indexes.REST.Endpoints == 0 {
		t.Errorf("rest index = %+v, want the embedded plane", report.Indexes.REST)
	}
	if !topN(report.Results, "system.tag.readBlocking", 3) {
		t.Errorf("readBlocking is not in the top 3: %+v", report.Results)
	}
	for _, r := range report.Results {
		if r.ModuleEnabled != nil {
			t.Errorf("%s carries module_enabled outside a Project Root", r.Name)
		}
		if r.Name == "system.tag.readBlocking" && (r.Scope != "all" || r.Module != "platform") {
			t.Errorf("readBlocking = %+v", r)
		}
	}
	env.AssertNoDockerOrphans(t)

	// A second run reads the cache: no container is created.
	before := len(env.DockerCalls(t))
	testrig.WantExit(t, env.RunIn(dir, "lookup", "write tag", "--json"), contract.ExitOK)
	for _, call := range env.DockerCalls(t)[before:] {
		if strings.Contains(strings.Join(call.Argv, " "), " create ") {
			t.Errorf("a cached lookup opened the image again: %v", call.Argv)
		}
	}

	detail := env.RunIn(dir, "lookup", "--name", "system.tag.readBlocking", "--json")
	testrig.WantExit(t, detail, contract.ExitOK)
	env.Golden(t, "lookup_name_function.json", detail.Stdout)
	human := env.RunIn(dir, "lookup", "--name", "system.tag.readBlocking")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "lookup_name_function.txt", human.Stdout)
}

// In a project with a running Gateway, the REST index is the Gateway's own
// OpenAPI document, each endpoint carries a ready-to-run call with the body's
// Content-Type, and --name prints the schemas.
func TestLookupReadsTheRunningGatewaysOpenAPI(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	lookupImage(t, env)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/openapi.json": 200})
	stub.SetBody("/openapi.json", lookupOpenAPI)

	res := env.RunIn(dir, "lookup", "import project", "--kind", "rest", "--limit", "3", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "lookup_rest.json", res.Stdout)
	var report lookupReport
	testrig.DataOf(t, res.Stdout, &report)
	if report.Indexes.REST.Source != "openapi" || report.Indexes.REST.Endpoints != 3 {
		t.Fatalf("rest index = %+v, want the live document's 3 endpoints", report.Indexes.REST)
	}
	first := report.Results[0]
	if first.Name != "POST /data/api/v1/projects/import/{name}" || first.ModuleEnabled == nil || !*first.ModuleEnabled ||
		!strings.Contains(first.Call, "--header 'Content-Type: application/zip'") {
		t.Errorf("first result = %+v", first)
	}

	detail := env.RunIn(dir, "lookup", "--name", "POST /data/api/v1/projects/import/demo", "--json")
	testrig.WantExit(t, detail, contract.ExitOK)
	var entry struct {
		Name      string         `json:"name"`
		Responses map[string]any `json:"responses"`
	}
	testrig.DataOf(t, detail.Stdout, &entry)
	if entry.Name != "POST /data/api/v1/projects/import/{name}" || !strings.Contains(detail.Stdout, `"changes"`) {
		t.Errorf("--name did not inline the response schema: %s", detail.Stdout)
	}

	// The cached live index is used without asking the Gateway again.
	hits := stub.Hits()
	testrig.WantExit(t, env.RunIn(dir, "lookup", "create project", "--kind", "rest", "--json"), contract.ExitOK)
	if stub.Hits() != hits {
		t.Errorf("a cached live index fetched /openapi.json again")
	}

	unknown := env.RunIn(dir, "lookup", "--name", "system.tag.nope", "--json")
	testrig.WantExit(t, unknown, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, unknown.Stdout), contract.CodeUnknownCapability)
}

// Without the official image, the function index is read from a Gateway image
// igdev built, which is FROM it; with no Ignition image at all, the fault names
// gateway ensure and docker pull.
func TestLookupReadsAnIgdevGatewayImageWhenTheOfficialOneIsMissing(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	lookupImage(t, env)
	dir := env.Mkdir("elsewhere")

	missing := env.Run(testrig.Run{Dir: dir, Args: []string{"lookup", "read tag values", "--json"}, Env: []string{"IGDEV_SHIM_IMAGE_MISSING=1"}})
	testrig.WantExit(t, missing, contract.ExitFailure)
	envelope := testrig.Envelope(t, missing.Stdout)
	testrig.WantCode(t, envelope, contract.CodeDocker)
	testrig.WantRemediation(t, envelope, "igdev gateway ensure")
	testrig.WantRemediation(t, envelope, "docker pull inductiveautomation/ignition:8.3.8")
	// The REST side needs no image.
	rest := env.Run(testrig.Run{Dir: dir, Args: []string{"lookup", "import project", "--kind", "rest", "--json"}, Env: []string{"IGDEV_SHIM_IMAGE_MISSING=1"}})
	testrig.WantExit(t, rest, contract.ExitOK)

	env.SetBaseEnv("IGDEV_SHIM_IMAGES=igdev-0badc0de:8.3.8")
	res := env.RunIn(dir, "lookup", "read tag values", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var report struct {
		Indexes struct {
			Functions struct {
				Image string `json:"image"`
			} `json:"functions"`
		} `json:"indexes"`
		Results []lookupResult `json:"results"`
	}
	testrig.DataOf(t, res.Stdout, &report)
	if report.Indexes.Functions.Image != "igdev-0badc0de:8.3.8" || !topN(report.Results, "system.tag.readBlocking", 3) {
		t.Errorf("image = %q, results = %+v", report.Indexes.Functions.Image, report.Results)
	}
}

func TestLookupUsage(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Mkdir("elsewhere")
	for _, args := range [][]string{
		{"lookup", "x", "--name", "system.tag.readBlocking"},
		{"lookup", "x", "--kind", "table"},
		{"lookup", "x", "--limit", "0"},
		{"lookup", "x", "y"},
	} {
		res := env.RunIn(dir, append(args, "--json")...)
		testrig.WantExit(t, res, contract.ExitUsage)
	}
	missing := env.RunIn(dir, "lookup", "--json")
	testrig.WantExit(t, missing, contract.ExitUsage)
	testrig.WantCode(t, testrig.Envelope(t, missing.Stdout), contract.CodeMissingArgument)
}

func topN(results []lookupResult, name string, n int) bool {
	for i, r := range results {
		if i >= n {
			break
		}
		if r.Name == name {
			return true
		}
	}
	return false
}
