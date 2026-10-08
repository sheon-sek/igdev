package itest

import (
	"archive/zip"
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The Gateway REST paths the running-Gateway verbs call.
const (
	healthyPath     = "/data/api/v1/modules/healthy"
	quarantinedPath = "/data/api/v1/modules/quarantined"
	uploadPath      = "/data/api/v1/modules/upload"
	certificatePath = "/data/api/v1/modules/certificate"
	installPath     = "/data/api/v1/modules/install"
)

// emptyItems is an Ignition list answer with nothing in it.
const emptyItems = `{"items":[],"metadata":{"total":0}}`

// moduleGateway serves a running Gateway's module routes from the given bodies.
func moduleGateway(t *testing.T, port int, healthy, quarantined string) *testrig.GatewayStub {
	t.Helper()
	stub := testrig.ServeGateway(t, port, map[string]int{
		healthyPath: 200, quarantinedPath: 200, uploadPath: 200, certificatePath: 200, installPath: 200,
	})
	stub.SetBody(healthyPath, healthy)
	stub.SetBody(quarantinedPath, quarantined)
	return stub
}

// restart restarts the container in place, waits, and reports the modules the
// Gateway lists; a module the checkout does not own may be quarantined.
func TestRestartReportsTheModules(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	moduleGateway(t, stamp.Ports.HTTP,
		`{"items":[{"id":"com.inductiveautomation.perspective","name":"Perspective","version":"3.3.8 (b1)","state":"ACTIVE","onStartup":"enabled","shouldUpgrade":false}]}`,
		`{"items":[{"id":"com.vendor.other","name":"Other","version":"1.0.0 (b0)","reason":"Certificate has not been accepted."}]}`)

	res := env.RunIn(dir, "restart", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "restart.json", res.Stdout)
	if got := gatewayVerbs(t, env); !containsVerb(got, "restart") {
		t.Errorf("restart did not restart the container: %v", got)
	}
	human := env.RunIn(dir, "restart")
	testrig.WantExit(t, human, contract.ExitOK)
	if !strings.Contains(human.Stdout, "1 active, 1 quarantined") || !strings.Contains(human.Stdout, "com.vendor.other") {
		t.Errorf("human report: %q", human.Stdout)
	}
}

// A quarantined module this checkout stages fails restart with the Gateway's
// reason, and an unsigned one names the contract switch.
func TestRestartFailsOnAQuarantinedStagedModule(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	source := modl(t, env, "build/acme.modl", "<BUILD>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", source), contract.ExitOK)
	moduleGateway(t, stamp.Ports.HTTP, emptyItems,
		`{"items":[{"id":"com.acme.vision","name":"Acme Vision","version":"1.2.3 (b0)","reason":"Module is unsigned and developer mode not enabled."}]}`)

	res := env.RunIn(dir, "restart", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeModuleQuarantined)
	if !strings.Contains(envelope.Message, "unsigned") || envelope.Remediation[0].Command != "igdev init --allow-unsigned-modules" {
		t.Errorf("quarantine fault: %q %v", envelope.Message, envelope.Remediation)
	}
}

// install uploads the archive, accepts the module's own certificate, installs it,
// stages it, and reports it running; nothing is restarted.
func TestModuleInstallHotInstallsAndStages(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	source := modl(t, env, "build/acme.modl", "<BUILD>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	stub := moduleGateway(t, stamp.Ports.HTTP,
		`{"items":[{"id":"com.acme.vision","name":"Acme Vision","version":"1.2.3 (b0)","state":"ACTIVE","onStartup":"enabled","shouldUpgrade":false}]}`,
		emptyItems)
	stub.SetBody(uploadPath, `{"moduleId":"com.acme.vision","licenseAccepted":true,"certAccepted":false,"containsEula":false,"containsCert":false}`)

	res := env.RunIn(dir, "module", "install", source, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "module_install.json", res.Stdout)
	want := []string{
		"POST " + uploadPath + "?fileName=acme.modl",
		"POST " + certificatePath + "?moduleId=com.acme.vision",
		"POST " + installPath + "?moduleId=com.acme.vision",
	}
	if got := stub.Requests(); len(got) < 3 || !reflect.DeepEqual(got[:3], want) {
		t.Errorf("requests = %v, want %v first", got, want)
	}
	if got := stub.ContentType(uploadPath); got != "application/octet-stream" {
		t.Errorf("upload Content-Type = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".igdev", "modules", "acme.modl")); err != nil {
		t.Errorf("install did not stage the artifact: %v", err)
	}
	if got := gatewayVerbs(t, env); containsVerb(got, "restart") {
		t.Errorf("a first install restarted the Gateway: %v", got)
	}
}

// A file that is not a module archive is refused before anything is sent.
func TestModuleInstallRefusesABadArchive(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	stub := moduleGateway(t, stamp.Ports.HTTP, emptyItems, emptyItems)
	bad := env.Path("build", "bad.modl")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := env.RunIn(dir, "module", "install", bad, "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeModuleArchiveInvalid)
	if len(stub.Requests()) != 0 {
		t.Errorf("a refused archive reached the Gateway: %v", stub.Requests())
	}
}

// build --install installs a changed artifact and skips one whose bytes were
// installed last time.
func TestBuildInstallSkipsUnchangedArtifacts(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract+`
[commands]
build = "true"

[modules]
artifacts = ["build/*.modl"]
`)
	modl(t, env, filepath.Join("repo", "build", "acme.modl"), "<ARTIFACT>", moduleXML("com.acme.vision", "Acme Vision", "1.2.3"))
	stub := moduleGateway(t, stamp.Ports.HTTP,
		`{"items":[{"id":"com.acme.vision","name":"Acme Vision","version":"1.2.3 (b0)","state":"ACTIVE","onStartup":"enabled"}]}`,
		emptyItems)
	stub.SetBody(uploadPath, `{"moduleId":"com.acme.vision","licenseAccepted":true,"certAccepted":true}`)

	first := env.RunIn(dir, "build", "--install", "--json")
	testrig.WantExit(t, first, contract.ExitOK)
	env.Golden(t, "build_install.json", first.Stdout)
	second := env.RunIn(dir, "build", "--install", "--json")
	testrig.WantExit(t, second, contract.ExitOK)
	if !strings.Contains(second.Stdout, `"action": "unchanged"`) {
		t.Errorf("an unchanged artifact was installed again: %s", second.Stdout)
	}
	uploads := 0
	for _, r := range stub.Requests() {
		if strings.HasPrefix(r, "POST "+uploadPath) {
			uploads++
		}
	}
	if uploads != 1 {
		t.Errorf("%d uploads, want 1", uploads)
	}
}

// import zips a project directory and posts it as application/zip; a name the
// Gateway already has is a usage error naming --overwrite.
func TestProjectImportZipsADirectory(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	project := projectDir(t, env)
	importPath := "/data/api/v1/projects/import/demo"
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{importPath: 200})
	stub.SetBody(importPath, `{"success":true,"changes":[{"name":"demo"}]}`)

	res := env.RunIn(dir, "project", "import", project, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "project_import.json", res.Stdout)
	if got := stub.ContentType(importPath); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if _, _, body := stub.LastRequest(); !strings.HasPrefix(body, "PK") {
		t.Error("the body is not a zip")
	}

	stub.SetStatus(importPath, http.StatusConflict)
	stub.SetBody(importPath, `{"success":false,"problem":{"message":"Name 'demo' already in use."}}`)
	conflict := env.RunIn(dir, "project", "import", project, "--json")
	testrig.WantExit(t, conflict, contract.ExitUsage)
	if !strings.Contains(conflict.Stdout, "--overwrite") {
		t.Errorf("the conflict does not name --overwrite: %s", conflict.Stdout)
	}
	overwrite := env.RunIn(dir, "project", "import", project, "--overwrite", "--json")
	testrig.WantExit(t, overwrite, contract.ExitUsage)
	if got := stub.Requests(); got[len(got)-1] != "POST "+importPath+"?overwrite=true" {
		t.Errorf("--overwrite sent %s", got[len(got)-1])
	}

	missing := env.Path("empty")
	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	testrig.WantExit(t, env.RunIn(dir, "project", "import", missing, "--json"), contract.ExitUsage)
}

// export unpacks the Gateway's zip into a directory, which then holds exactly the
// project's files.
func TestProjectExportUnpacksIntoADirectory(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	exportPath := "/data/api/v1/projects/export/demo"
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{exportPath: 200})
	stub.SetBody(exportPath, zipOf(t, map[string]string{"project.json": `{"title":"demo"}`, "a/code.py": "x = 1\n"}))

	out := env.Path("out", "demo")
	res := env.RunIn(dir, "project", "export", "demo", "--output", out, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.RegisterReplacement(out, "<OUT>")
	env.Golden(t, "project_export.json", res.Stdout)
	if got := readFile(t, filepath.Join(out, "a", "code.py")); got != "x = 1\n" {
		t.Errorf("code.py = %q", got)
	}
	missing := env.RunIn(dir, "project", "export", "nope", "--json")
	testrig.WantExit(t, missing, contract.ExitUsage)
}

// projectDir writes a small project directory.
func projectDir(t *testing.T, env *testrig.Env) string {
	t.Helper()
	root := env.Path("projects", "demo")
	for name, body := range map[string]string{
		"project.json": `{"title":"demo"}`, "ignition/script-python/a/code.py": "x = 1\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env.RegisterReplacement(root, "<PROJECT>")
	return root
}

// zipOf builds a zip in memory.
func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// containsVerb reports whether any recorded engine call ran verb.
func containsVerb(verbs []string, verb string) bool {
	for _, v := range verbs {
		if strings.HasSuffix(v, ":"+verb) || strings.HasSuffix(v, " "+verb) {
			return true
		}
	}
	return false
}
