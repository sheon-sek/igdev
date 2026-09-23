package itest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/capacity"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/runtimeassets"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The Gateway lifecycle, asserted through seam S1: the real binary, a fake
// container engine that keeps the objects a compose project owns, a shimmed memory
// reading, and a loopback server standing in for the Gateway's web server. Every
// port that appears in output is the one the Checkout Setup recorded, and the
// recorded pair is pinned per test so the golden can be a literal.

// smokeContract declares the endpoints a project wants checked; the minimal
// contract declares none, which is the root-document-only case.
const smokeContract = `schema = 1

[project]
name = "fixture"

[gateway]
memory_mb = 2048
timezone = "UTC"
smoke_endpoints = ["/system/gateway/info", "/data/api/v1/gateway-info"]
`

// strictConsentContract opts into the human gate for the private modules it stages
// (ADR 0006).
const strictConsentContract = `schema = 1

[project]
name = "fixture"

[modules]
enabled = []
require_private_module_consent = true

[gateway]
memory_mb = 2048
timezone = "UTC"
`

// freeTriplet asks the kernel for a port triplet the test then pins into the
// Checkout Setup, so a gateway test knows its own addresses.
func freeTriplet(t *testing.T) ports.Triplet {
	t.Helper()
	triplet, fault := ports.Allocate()
	if fault != nil {
		t.Fatalf("allocate a fixed port triplet: %v", fault)
	}
	return triplet
}

// pinPorts rewrites the recorded triplet, re-rendering the runtime files so they
// describe the same Instance. It stands in for the random allocation setup makes,
// which is what lets a test assert an exact URL.
func pinPorts(t *testing.T, env *testrig.Env, dir string, triplet ports.Triplet) gate.Stamp {
	t.Helper()
	stamp := setupStampOf(t, dir)
	previous := stamp.Ports
	stamp.Ports = triplet
	raw, err := stamp.Encode()
	if err != nil {
		t.Fatalf("encode the Checkout Setup record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, project.StateDir, project.SetupRecord), raw, 0o600); err != nil {
		t.Fatalf("rewrite the Checkout Setup record: %v", err)
	}
	replacer := strings.NewReplacer(
		strconv.Itoa(previous.HTTP), strconv.Itoa(triplet.HTTP),
		strconv.Itoa(previous.HTTPS), strconv.Itoa(triplet.HTTPS),
		strconv.Itoa(previous.Debug), strconv.Itoa(triplet.Debug),
	)
	for _, name := range []string{runtimeassets.ComposeFileName, runtimeassets.EnvFileName} {
		path := filepath.Join(dir, project.StateDir, "runtime", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(replacer.Replace(string(body))), 0o644); err != nil {
			t.Fatalf("rewrite %s: %v", path, err)
		}
	}
	return stamp
}

// gatewayFixture materializes one Instance: a Project Contract, a Checkout Setup
// with Consent, and a pinned port triplet. The random identity and ports collapse
// to tokens so a golden can be a literal.
func gatewayFixture(t *testing.T, env *testrig.Env, body string) (string, gate.Stamp) {
	t.Helper()
	dir := env.Project("repo", body)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	stamp := pinPorts(t, env, dir, freeTriplet(t))
	normalizeInstance(t, env, stamp)
	normalizeConsent(t, env)
	return dir, stamp
}

// composePaths is where the Checkout Setup renders this Instance's Compose files.
func composePaths(dir string) (compose, envFile string) {
	runtime := filepath.Join(dir, project.StateDir, "runtime")
	return filepath.Join(runtime, runtimeassets.ComposeFileName), filepath.Join(runtime, runtimeassets.EnvFileName)
}

// gatewayVerbs condenses the recorded engine calls into "<project>:<verb>" rows.
func gatewayVerbs(t *testing.T, env *testrig.Env) []string {
	t.Helper()
	verbs := []string{}
	for _, call := range env.DockerCalls(t) {
		argv := call.Argv
		if len(argv) > 0 && filepath.Base(argv[0]) == "docker" {
			argv = argv[1:]
		}
		if len(argv) == 0 || argv[0] != "compose" {
			verbs = append(verbs, strings.Join(argv, " "))
			continue
		}
		project := ""
		for i, arg := range argv {
			if arg == "--project-name" && i+1 < len(argv) {
				project = argv[i+1]
			}
		}
		verbs = append(verbs, project+":"+testrig.ComposeVerb(argv[1:]))
	}
	return verbs
}

// Consent is machine state and it is required before anything touches a runtime:
// without the record every verb is the frozen human-required shape, and no engine
// call happens.
func TestGatewayRequiresConsent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)

	// A machine whose Consent record was never written (or was removed) has not
	// accepted the EULA, whatever the checkout holds.
	if err := os.Remove(consent.Path(filepath.Join(env.Home, ".config", "igdev"))); err != nil {
		t.Fatalf("remove the Consent record: %v", err)
	}
	for _, action := range []string{"up", "url", "status"} {
		t.Run(action, func(t *testing.T) {
			res := env.RunIn(dir, "gateway", action, "--json")
			testrig.WantExit(t, res, contract.ExitHumanAction)
			envelope := testrig.Envelope(t, res.Stdout)
			testrig.WantCode(t, envelope, contract.CodeConsentRequired)
			testrig.WantRemediation(t, envelope, "igdev setup --accept-eula")
		})
	}
	env.AssertNoDockerCalls(t)
}

// A checkout that was never materialized has no Instance to control: the Gate
// refuses before any engine call and names the repair.
func TestGatewayRequiresSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	env.Project("repo", testrig.MinimalContract)
	dir := env.Path("repo")

	res := env.RunIn(dir, "gateway", "url", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeSetupRequired)
	testrig.WantRemediation(t, envelope, "igdev setup")
	env.AssertNoDockerCalls(t)
}

// url/is the recorded address and nothing else: the port in setup.json, never the
// container-internal 8088 any output would otherwise be tempted to assume.
func TestGatewayURLUsesTheRecordedPorts(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)

	res := env.RunIn(dir, "gateway", "url", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_url.json", res.Stdout)

	var data struct {
		URL       string        `json:"url"`
		Namespace string        `json:"namespace"`
		Ports     ports.Triplet `json:"ports"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if want := fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP); data.URL != want {
		t.Errorf("url = %q, want the recorded %q", data.URL, want)
	}
	if data.Ports != stamp.Ports {
		t.Errorf("ports = %+v, want the recorded %+v", data.Ports, stamp.Ports)
	}
	if data.Namespace != stamp.Namespace() {
		t.Errorf("namespace = %q, want %q", data.Namespace, stamp.Namespace())
	}
	if strings.Contains(res.Stdout, "8088") {
		t.Errorf("url output assumes port 8088:\n%s", res.Stdout)
	}

	// Human mode prints the bare URL, so it is usable in a command substitution.
	human := env.RunIn(dir, "gateway", "url")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "gateway_url.txt", human.Stdout)
	if strings.TrimSpace(human.Stdout) != data.URL {
		t.Errorf("human url = %q, want %q", human.Stdout, data.URL)
	}
	env.AssertNoDockerCalls(t)
}

// up addresses one compose project by name, with this Instance's Compose file and
// environment, and the published ports are the recorded triplet.
func TestGatewayUpStartsTheNamespacedProject(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)

	res := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_up.json", res.Stdout)

	var data struct {
		URL       string        `json:"url"`
		Namespace string        `json:"namespace"`
		Ports     ports.Triplet `json:"ports"`
		Capacity  struct {
			Measured    bool `json:"measured"`
			AvailableMB int  `json:"available_mb"`
			RequiredMB  int  `json:"required_mb"`
			HeadroomMB  int  `json:"headroom_mb"`
			Forced      bool `json:"forced"`
		} `json:"capacity"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if want := fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP); data.URL != want {
		t.Errorf("url = %q, want %q", data.URL, want)
	}
	if data.Namespace != stamp.Namespace() {
		t.Errorf("namespace = %q, want %q", data.Namespace, stamp.Namespace())
	}
	if !data.Capacity.Measured || data.Capacity.AvailableMB != 65536 ||
		data.Capacity.RequiredMB != 2048+512 || data.Capacity.HeadroomMB != 512 || data.Capacity.Forced {
		t.Errorf("capacity = %+v, want the measured 65536 MiB host and a 2560 MiB requirement", data.Capacity)
	}

	composeFile, envFile := composePaths(dir)
	calls := env.DockerCalls(t)
	if len(calls) != 1 {
		t.Fatalf("up made %d engine calls, want 1: %+v", len(calls), calls)
	}
	want := []string{
		"compose", "--project-name", stamp.Namespace(),
		"--file", composeFile, "--env-file", envFile,
		"up", "--detach", "--build", "gateway",
	}
	if !slices.Equal(calls[0].Argv, want) {
		t.Errorf("up argv =\n  %v\nwant\n  %v", calls[0].Argv, want)
	}

	// The published ports in the rendered Compose file are the recorded triplet.
	body, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatalf("read the Compose file: %v", err)
	}
	for _, published := range []struct{ host, container int }{
		{stamp.Ports.HTTP, 8088}, {stamp.Ports.HTTPS, 8043}, {stamp.Ports.Debug, 8000},
	} {
		mapping := fmt.Sprintf("%q", fmt.Sprintf("127.0.0.1:%d:%d", published.host, published.container))
		if !strings.Contains(string(body), "      - "+mapping) {
			t.Errorf("Compose file does not publish %s:\n%s", mapping, body)
		}
	}

	// The project owns a container and a named volume, both namespaced.
	objects := filepath.Join(env.Path("state"), "docker")
	for _, path := range []string{
		filepath.Join(objects, "containers", testrig.ComposeContainer(stamp.Namespace())),
		filepath.Join(objects, "volumes", testrig.ComposeVolume(stamp.Namespace())),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("compose up did not register %s: %v", path, err)
		}
	}

	// down --volumes retires both, so the fixture leaves no docker litter.
	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// The private modules a checkout stages are its own artifacts, so starting a
// Gateway accepts them by module id — no human step for the module's license or
// certificate (ADR 0006). Built-ins are never named: only the staging directory is
// read.
func TestGatewayAcceptsTheStagedPrivateModules(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)
	for _, staged := range []struct{ rel, id string }{
		{"downloads/acme-vision.modl", "com.acme.vision"},
		{"downloads/acme-plc.modl", "com.acme.plc"},
	} {
		path := modl(t, env, staged.rel, "<MODL_"+staged.id+">", moduleXML(staged.id, "Acme", "1.0.0"))
		testrig.WantExit(t, env.RunIn(dir, "module", "add", path), contract.ExitOK)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "up", "--json"), contract.ExitOK)
	// The image reads both variables as a comma-separated list of module ids, in
	// the order the staging directory reports them.
	const want = "com.acme.plc,com.acme.vision"
	call := env.DockerCalls(t)[0]
	for _, name := range []string{"ACCEPT_MODULE_LICENSES", "ACCEPT_MODULE_CERTS"} {
		if got := call.GatewayEnv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// A checkout that stages nothing names nothing.
	testrig.WantExit(t, env.RunIn(dir, "module", "clear"), contract.ExitOK)
	empty := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, empty, contract.ExitOK)
	calls := env.DockerCalls(t)
	last := calls[len(calls)-1]
	for _, name := range []string{"ACCEPT_MODULE_LICENSES", "ACCEPT_MODULE_CERTS"} {
		if got := last.GatewayEnv(name); got != "" {
			t.Errorf("%s = %q with nothing staged, want empty", name, got)
		}
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// [modules] require_private_module_consent puts the machine-global terms back in
// front of that: nothing is passed until a person recorded both, and the refusal
// names both accept commands so one pass records them (ADR 0004, ADR 0006).
func TestGatewayRequiresConsentForStagedPrivateModules(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, strictConsentContract)
	path := modl(t, env, "downloads/acme-vision.modl", "<MODL>", moduleXML("com.acme.vision", "Acme Vision", "1.0.0"))
	testrig.WantExit(t, env.RunIn(dir, "module", "add", path), contract.ExitOK)

	// The EULA is on record (the fixture recorded it); the module terms are not.
	refused := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, refused, contract.ExitHumanAction)
	envelope := testrig.Envelope(t, refused.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConsentRequired)
	testrig.WantRemediation(t, envelope, "igdev setup --accept-module-license")
	testrig.WantRemediation(t, envelope, "igdev setup --accept-module-certificate")
	env.AssertNoDockerCalls(t)

	// A person records both terms; then the id is passed like any other.
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-module-license", "--accept-module-certificate"), contract.ExitOK)
	accepted := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, accepted, contract.ExitOK)
	if got := env.DockerCalls(t)[0].GatewayEnv("ACCEPT_MODULE_LICENSES"); got != "com.acme.vision" {
		t.Errorf("ACCEPT_MODULE_LICENSES = %q, want the staged module after the human accepted", got)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// status reports what the engine says about this Instance plus the recorded URL,
// in both the running and the not-started case.
func TestGatewayStatusReportsComposeState(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)

	absent := env.RunIn(dir, "gateway", "status", "--json")
	testrig.WantExit(t, absent, contract.ExitOK)
	env.Golden(t, "gateway_status_absent.json", absent.Stdout)
	var before struct {
		State    string `json:"state"`
		Services []struct {
			Name string `json:"name"`
		} `json:"services"`
	}
	testrig.DataOf(t, absent.Stdout, &before)
	if before.State != "absent" || len(before.Services) != 0 {
		t.Errorf("status before up = %+v, want an absent project", before)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	res := env.RunIn(dir, "gateway", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_status.json", res.Stdout)
	var data struct {
		URL       string        `json:"url"`
		Namespace string        `json:"namespace"`
		Ports     ports.Triplet `json:"ports"`
		State     string        `json:"state"`
		Services  []struct {
			Service string `json:"service"`
			State   string `json:"state"`
			Status  string `json:"status"`
		} `json:"services"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.State != "running" {
		t.Errorf("state = %q, want running", data.State)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP); data.URL != want {
		t.Errorf("url = %q, want the recorded %q", data.URL, want)
	}
	// The published port the engine reports is the recorded one, not 8088.
	if len(data.Services) != 1 || data.Services[0].Service != "gateway" || data.Services[0].State != "running" {
		t.Errorf("services = %+v, want the running gateway service", data.Services)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// wait polls the recorded URL: it succeeds against a listening stand-in Gateway,
// and a Gateway that never answers fails at exit 1 with the endpoint named and the
// engine's log tail on stderr.
func TestGatewayWaitPollsTheRecordedURL(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/": 200})
	ready := env.RunIn(dir, "gateway", "wait", "--timeout", "5", "--json")
	testrig.WantExit(t, ready, contract.ExitOK)
	env.Golden(t, "gateway_wait.json", ready.Stdout)
	if stub.Hits() == 0 {
		t.Error("wait never polled the recorded URL")
	}
	stub.Close()

	// Nothing listens any more: the same wait fails, names the URL, and reports
	// the Gateway's own log so the reason is not a mystery.
	failed := env.RunIn(dir, "gateway", "wait", "--timeout", "1", "--json")
	testrig.WantExit(t, failed, contract.ExitFailure)
	envelope := testrig.Envelope(t, failed.Stdout)
	testrig.WantCode(t, envelope, contract.CodeGatewayUnhealthy)
	testrig.WantRemediation(t, envelope, "igdev gateway logs --tail 50")
	if !strings.Contains(envelope.Message, fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP)) {
		t.Errorf("failure does not name the recorded URL: %q", envelope.Message)
	}
	if !strings.Contains(failed.Stderr, "gateway log line 1") {
		t.Errorf("failure did not report the Gateway log tail:\n%s", failed.Stderr)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// smoke checks the root document plus the endpoints the Project Contract declares,
// and a failing endpoint is named in the failure.
func TestGatewaySmokeChecksDeclaredEndpoints(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, smokeContract)

	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{
		"/":                         200,
		"/system/gateway/info":      200,
		"/data/api/v1/gateway-info": 200,
	})
	res := env.RunIn(dir, "gateway", "smoke", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_smoke.json", res.Stdout)

	var data struct {
		URL    string `json:"url"`
		Checks []struct {
			Path   string `json:"path"`
			URL    string `json:"url"`
			Status int    `json:"status"`
			OK     bool   `json:"ok"`
		} `json:"checks"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	paths := []string{"/", "/system/gateway/info", "/data/api/v1/gateway-info"}
	if len(data.Checks) != len(paths) {
		t.Fatalf("smoke checked %d endpoints, want %d: %+v", len(data.Checks), len(paths), data.Checks)
	}
	for i, path := range paths {
		if data.Checks[i].Path != path {
			t.Errorf("check %d = %q, want %q (declared order)", i, data.Checks[i].Path, path)
		}
		if !data.Checks[i].OK || data.Checks[i].Status != 200 {
			t.Errorf("check %d = %+v, want a 200", i, data.Checks[i])
		}
		if want := fmt.Sprintf("http://127.0.0.1:%d%s", stamp.Ports.HTTP, path); data.Checks[i].URL != want {
			t.Errorf("check %d url = %q, want %q", i, data.Checks[i].URL, want)
		}
	}

	// One endpoint starts answering 500: smoke fails and says which one.
	stub.SetStatus("/system/gateway/info", 500)
	failed := env.RunIn(dir, "gateway", "smoke", "--json")
	testrig.WantExit(t, failed, contract.ExitFailure)
	envelope := testrig.Envelope(t, failed.Stdout)
	testrig.WantCode(t, envelope, contract.CodeGatewayUnhealthy)
	if !strings.Contains(envelope.Message, "/system/gateway/info") ||
		!strings.Contains(envelope.Message, "500") {
		t.Errorf("failure does not name the failing endpoint: %q", envelope.Message)
	}
	if !strings.Contains(envelope.Message, "2 of 3 checks passed") {
		t.Errorf("failure does not report the pass count: %q", envelope.Message)
	}
}

// A contract that declares no endpoints checks the root document alone.
func TestGatewaySmokeWithoutDeclaredEndpoints(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/": 200})

	res := env.RunIn(dir, "gateway", "smoke", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	var data struct {
		Checks []struct {
			Path string `json:"path"`
		} `json:"checks"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if len(data.Checks) != 1 || data.Checks[0].Path != "/" {
		t.Errorf("checks = %+v, want the root document alone", data.Checks)
	}
}

// The Capacity Gate refuses a new Gateway against a shimmed memory reading, names
// the running instance that holds the memory, and --force bypasses it with a
// warning.
func TestGatewayCapacityGateRefusesAndForceOverrides(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	first, firstStamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(first, "gateway", "up"), contract.ExitOK)

	// The machine is now short: 1024 MiB free against a 2048 MiB heap plus 512 MiB
	// headroom.
	env.ShimMeminfo(1024)
	second := env.Project("second", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(second, "setup", "--accept-eula"), contract.ExitOK)
	normalizeInstance(t, env, setupStampOf(t, second))

	res := env.RunIn(second, "gateway", "up", "--json")
	testrig.WantExit(t, res, contract.ExitHumanAction)
	env.Golden(t, "gateway_capacity.json", res.Stdout)

	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeCapacity)
	testrig.WantRemediation(t, envelope, "igdev gateway up --force")
	if !strings.Contains(envelope.Message, "1024 MiB available") ||
		!strings.Contains(envelope.Message, "2560 MiB required") {
		t.Errorf("refusal does not report the arithmetic: %q", envelope.Message)
	}
	if !strings.Contains(envelope.Message, firstStamp.Namespace()) {
		t.Errorf("refusal does not name the running instance %s: %q", firstStamp.Namespace(), envelope.Message)
	}
	// The refused run never asked the engine to start anything.
	if verbs := gatewayVerbs(t, env); slices.Contains(verbs, namespaceOf(t, second)+":up") {
		t.Errorf("a refused up still started the project: %v", verbs)
	}

	// --force starts it anyway, with the warning a person has to see.
	forced := env.RunIn(second, "gateway", "up", "--force", "--json")
	testrig.WantExit(t, forced, contract.ExitOK)
	if !strings.Contains(forced.Stderr, "bypassed") {
		t.Errorf("--force did not warn on stderr: %q", forced.Stderr)
	}
	var data struct {
		Capacity struct {
			Forced bool `json:"forced"`
		} `json:"capacity"`
	}
	testrig.DataOf(t, forced.Stdout, &data)
	if !data.Capacity.Forced {
		t.Error("--force did not report the bypass")
	}

	for _, dir := range []string{first, second} {
		testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	}
	env.AssertNoDockerOrphans(t)
}

// secondNamespace is the Docker namespace a fixture's Checkout Setup recorded.
func namespaceOf(t *testing.T, dir string) string {
	t.Helper()
	return setupStampOf(t, dir).Namespace()
}

// reset is exactly down --volumes, then up, then wait: the engine sees that
// sequence, in that order, and the Instance keeps the ports it had.
func TestGatewayResetIsDownUpAndWait(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	stub := testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/": 200})

	res := env.RunIn(dir, "gateway", "reset", "--timeout", "5", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_reset.json", res.Stdout)

	want := []string{
		stamp.Namespace() + ":up",
		stamp.Namespace() + ":down",
		stamp.Namespace() + ":up",
	}
	if got := gatewayVerbs(t, env); !slices.Equal(got, want) {
		t.Errorf("reset engine calls = %v, want %v", got, want)
	}
	// down --volumes is in the argv, not only implied by the verb.
	down := env.DockerCalls(t)[1].Argv
	if !slices.Contains(down, "--volumes") {
		t.Errorf("reset's down does not remove the volume: %v", down)
	}
	// reset ends with a wait, so the stand-in Gateway was polled.
	if stub.Hits() == 0 {
		t.Error("reset did not wait for the Gateway to answer")
	}

	var data struct {
		URL   string        `json:"url"`
		Ports ports.Triplet `json:"ports"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Ports != stamp.Ports || data.URL != fmt.Sprintf("http://127.0.0.1:%d", stamp.Ports.HTTP) {
		t.Errorf("reset changed the recorded addressing: %+v", data)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// logs is Compose's log through igdev: prose on stdout for humans, the envelope's
// data member for agents.
func TestGatewayLogsPassesThroughComposeLogs(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)

	human := env.RunIn(dir, "gateway", "logs", "--tail", "200")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "gateway_logs.txt", human.Stdout)

	machine := env.RunIn(dir, "gateway", "logs", "--tail", "50", "--json")
	testrig.WantExit(t, machine, contract.ExitOK)
	env.Golden(t, "gateway_logs.json", machine.Stdout)
	var data struct {
		Logs string `json:"logs"`
	}
	testrig.DataOf(t, machine.Stdout, &data)
	if !strings.Contains(data.Logs, "gateway log line 1") {
		t.Errorf("logs = %q, want the Gateway's log", data.Logs)
	}
	last := env.DockerCalls(t)[len(env.DockerCalls(t))-1]
	if !slices.Contains(last.Argv, "--tail") || !strings.Contains(strings.Join(last.Argv, " "), "logs --tail 50 gateway") {
		t.Errorf("logs argv = %v, want the tail passed to compose", last.Argv)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// The admin password is served only by `gateway credentials --json`; every human
// command reports the username and where the secret lives, never the secret.
func TestGatewayCredentialsAreMachineOnly(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir := env.Project("repo", testrig.MinimalContract)
	const secret = "shim-secret-password"
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula", "--admin-password", secret), contract.ExitOK)
	stamp := setupStampOf(t, dir)
	normalizeInstance(t, env, stamp)
	normalizeConsent(t, env)

	res := env.RunIn(dir, "gateway", "credentials", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "gateway_credentials.json", res.Stdout)
	var data struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Username != "admin" || data.Password != secret {
		t.Errorf("credentials = %+v, want the recorded admin pair", data)
	}

	// Human output reports where the credential lives and never the credential.
	human := env.RunIn(dir, "gateway", "credentials")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "gateway_credentials.txt", human.Stdout)
	if strings.Contains(human.Stdout+human.Stderr, secret) {
		t.Errorf("human credentials leaked the password:\n%s\n%s", human.Stdout, human.Stderr)
	}
	if !strings.Contains(human.Stdout, "admin") || !strings.Contains(human.Stdout, "local.toml") {
		t.Errorf("human credentials do not report the username and source:\n%s", human.Stdout)
	}

	// No other human verb leaks it either: it travels in the process environment
	// and the 0600 file, nowhere else.
	for _, args := range [][]string{
		{"gateway", "url"},
		{"gateway", "up"},
		{"gateway", "status"},
		{"status"},
	} {
		res := env.RunIn(dir, args...)
		if strings.Contains(res.Stdout+res.Stderr, secret) {
			t.Errorf("%v leaked the admin password", args)
		}
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// The frozen engine call sequence of one full lifecycle, as a golden: the project
// name, the two rendered files, the verb, and the objects the shim recorded. A
// golden is the review surface a reviewer reads when the addressing changes.
func TestGatewayEngineCallSequence(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)

	for _, args := range [][]string{
		{"gateway", "up"},
		{"gateway", "status"},
		{"gateway", "logs", "--tail", "50"},
		{"gateway", "restart"},
		{"gateway", "down", "--volumes"},
	} {
		testrig.WantExit(t, env.RunIn(dir, args...), contract.ExitOK)
	}

	lines := []string{}
	for _, call := range env.DockerCalls(t) {
		argv := call.Argv
		if len(argv) > 0 && filepath.Base(argv[0]) == "docker" {
			argv = argv[1:]
		}
		lines = append(lines, strings.Join(argv, " "))
	}
	env.Golden(t, "gateway_calls.txt", strings.Join(lines, "\n")+"\n")

	// The state the shim recorded is the Instance's: one container, one named
	// volume, and after down --volumes neither.
	objects := filepath.Join(env.Path("state"), "docker")
	if entries, err := os.ReadDir(filepath.Join(objects, "containers")); err != nil || len(entries) != 0 {
		t.Errorf("containers after down --volumes = %v (err %v), want none", entries, err)
	}
	if entries, err := os.ReadDir(filepath.Join(objects, "volumes")); err != nil || len(entries) != 0 {
		t.Errorf("volumes after down --volumes = %v (err %v), want none", entries, err)
	}
	env.AssertNoDockerOrphans(t)
}

// A host whose free memory cannot be measured is never refused: there is no
// evidence of a shortage, and the warning says the guard did not apply.
func TestGatewayCapacityGateUnmeasuredWarns(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	env.SetBaseEnv(capacity.MeminfoEnv + "=" + env.Path("state", "no-such-meminfo"))

	res := env.RunIn(dir, "gateway", "up", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stderr, "cannot measure free memory") {
		t.Errorf("an unmeasured host did not warn on stderr: %q", res.Stderr)
	}
	var data struct {
		Capacity struct {
			Measured bool `json:"measured"`
		} `json:"capacity"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Capacity.Measured {
		t.Error("an unmeasured host reported a measurement")
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// A checkout with no recorded password has no credential to serve, and the repair
// is setup rather than an empty answer.
func TestGatewayCredentialsWithoutARecord(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)
	if err := os.Remove(filepath.Join(dir, project.StateDir, project.LocalConfig)); err != nil {
		t.Fatalf("remove the checkout-local tier: %v", err)
	}

	res := env.RunIn(dir, "gateway", "credentials", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConfigInvalid)
	testrig.WantRemediation(t, envelope, "igdev setup")
}

// An unknown gateway action is a usage error naming the command, not a help dump.
func TestGatewayUnknownAction(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	gatewayFixture(t, env, testrig.MinimalContract)

	dir := env.Path("repo")
	res := env.RunIn(dir, "gateway", "restart-everything", "--json")
	testrig.WantExit(t, res, contract.ExitUsage)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeUsage)
	if !strings.Contains(envelope.Message, "igdev gateway") {
		t.Errorf("message does not name the command: %q", envelope.Message)
	}
	env.AssertNoDockerCalls(t)
}
