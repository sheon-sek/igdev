package runtimeassets

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/ports"
)

func sampleInput() Input {
	return Input{
		InstanceID:      "3b1f0c2a-9d4e-4f6b-8a11-0c2d3e4f5a6b",
		Namespace:       "igdev-3b1f0c2a",
		IgnitionVersion: "8.3.8",
		JythonVersion:   "2.7.4",
		Edition:         "standard",
		Modules:         []string{"com.inductiveautomation.perspective", "com.inductiveautomation.opcua"},
		MemoryMB:        2048,
		Timezone:        "UTC",
		Ports:           ports.Triplet{HTTP: 18070, HTTPS: 19070, Debug: 20070},
		RuntimeDir:      "/repo/.igdev/runtime",
		ModulesDir:      "/repo/.igdev/modules",
		BaselineDir:     "/repo/.igdev/baseline",
	}
}

// Determinism is the property that makes re-setup idempotent: identical inputs
// must produce byte-identical files, which rules out any timestamp or random
// value in a rendered template.
func TestRenderingIsDeterministic(t *testing.T) {
	timestamp := regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}`)
	for _, tc := range []struct {
		name string
		gen  func(Input) ([]byte, error)
	}{
		{"compose", Compose},
		{"compose.env", ComposeEnv},
		{"Dockerfile", Dockerfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := tc.gen(sampleInput())
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			second, err := tc.gen(sampleInput())
			if err != nil {
				t.Fatalf("render again: %v", err)
			}
			if string(first) != string(second) {
				t.Errorf("rendering twice differed:\n--- first ---\n%s\n--- second ---\n%s", first, second)
			}
			if match := timestamp.FindString(string(first)); match != "" {
				t.Errorf("rendered file carries a timestamp (%q); only setup.json and accepted.toml may", match)
			}
		})
	}
}

// The Compose file has to carry the Instance's namespace, the allocated ports,
// the staged-modules and Baseline mounts, the Baseline restore wiring, and the
// self-contained build context: that is what makes a Gateway reachable without
// guessing 8088.
func TestComposeCarriesNamespacePortsAndMounts(t *testing.T) {
	raw, err := Compose(sampleInput())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"name: igdev-3b1f0c2a",
		"context: /repo/.igdev/runtime",
		"dockerfile: Dockerfile",
		"image: igdev-3b1f0c2a:8.3.8",
		`"127.0.0.1:18070:8088"`,
		`"127.0.0.1:19070:8043"`,
		`"127.0.0.1:20070:8000"`,
		"/repo/.igdev/baseline:/restore:ro",
		`"host.docker.internal:host-gateway"`,
		`GATEWAY_MODULES_ENABLED: "com.inductiveautomation.perspective,com.inductiveautomation.opcua"`,
		`TZ: "UTC"`,
		"-n igdev-3b1f0c2a",
		"-m 2048",
		// The Gateway's launcher command carries the staged Baseline's restore
		// arguments, which the process environment supplies (empty when nothing is
		// staged) — so this file never has to be re-rendered when it changes.
		"${GATEWAY_RESTORE_ARGS:-}",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("compose file does not carry %q:\n%s", want, body)
		}
	}
	// Nothing is staged, so nothing is mounted over the image's modules directory.
	if strings.Contains(body, "user-lib/modules") {
		t.Errorf("compose file mounts over the modules directory with nothing staged:\n%s", body)
	}
	// Ports are published on loopback only.
	if strings.Contains(body, "0.0.0.0") {
		t.Errorf("compose file publishes outside loopback:\n%s", body)
	}
}

// No rendered runtime file may carry a credential. The structural proof is that
// Input has no password field at all; this pins the visible consequence — nothing
// renders an assignment of one — so the files stay safe to diff, to print, and to
// freeze in a golden. A reference to the Compose variable the process environment
// supplies (`${GATEWAY_ADMIN_PASSWORD}`) is not a value.
func TestRenderedFilesAssignNoCredentials(t *testing.T) {
	assignment := regexp.MustCompile(`(?i)admin_?password\s*[:=]\s*[^$\s{}]`)
	for _, tc := range []struct {
		name string
		gen  func(Input) ([]byte, error)
	}{
		{"compose", Compose},
		{"compose.env", ComposeEnv},
		{"Dockerfile", Dockerfile},
	} {
		raw, err := tc.gen(sampleInput())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if match := assignment.FindString(string(raw)); match != "" {
			t.Errorf("%s assigns a credential (%q); secrets stay in the 0600 tier", tc.name, match)
		}
	}
}

// The Compose environment is what docker compose reads with --env-file, so it
// has to state the ports, the namespace, and the Instance identity.
func TestComposeEnvCarriesTheRuntimeSettings(t *testing.T) {
	raw, err := ComposeEnv(sampleInput())
	if err != nil {
		t.Fatalf("ComposeEnv: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"COMPOSE_PROJECT_NAME=igdev-3b1f0c2a",
		"GATEWAY_NAME=igdev-3b1f0c2a",
		"GATEWAY_HTTP_PORT=18070",
		"GATEWAY_HTTPS_PORT=19070",
		"GATEWAY_DEBUG_PORT=20070",
		"GATEWAY_MAX_MEMORY_MB=2048",
		"IGDEV_INSTANCE_ID=3b1f0c2a-9d4e-4f6b-8a11-0c2d3e4f5a6b",
		"IGDEV_MODULES_PATH=/repo/.igdev/modules",
		"IGDEV_BASELINE_PATH=/repo/.igdev/baseline",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("compose.env does not carry %q:\n%s", want, body)
		}
	}
}

// The unsigned-modules switch is rendered from the contract's effective value:
// true unless the contract states false (ADR 0006, amendment 1).
func TestComposeEnvRendersTheUnsignedModulesSwitch(t *testing.T) {
	off, err := ComposeEnv(sampleInput())
	if err != nil {
		t.Fatalf("ComposeEnv: %v", err)
	}
	if !strings.Contains(string(off), "IGNITION_ALLOW_UNSIGNED_MODULES=false") {
		t.Errorf("compose.env does not carry the default switch:\n%s", off)
	}

	on := sampleInput()
	on.AllowUnsignedModules = true
	raw, err := ComposeEnv(on)
	if err != nil {
		t.Fatalf("ComposeEnv: %v", err)
	}
	if !strings.Contains(string(raw), "IGNITION_ALLOW_UNSIGNED_MODULES=true") {
		t.Errorf("compose.env does not render the contract's switch:\n%s", raw)
	}
}

// The Dockerfile derives from the Ignition image the contract asked for, and
// copies nothing out of the repository: the build context is the disposable
// runtime directory alone, and the only files it copies are the ones setup
// generates there — the seed and the trial keeper script.
func TestDockerfilePinsTheRequestedImage(t *testing.T) {
	raw, err := Dockerfile(sampleInput())
	if err != nil {
		t.Fatalf("Dockerfile: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "FROM inductiveautomation/ignition:${IGNITION_VERSION}") {
		t.Errorf("Dockerfile does not derive from the Ignition image:\n%s", body)
	}
	if !strings.Contains(body, "ARG IGNITION_VERSION=8.3.8") {
		t.Errorf("Dockerfile does not default the image tag from the contract:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "COPY") && line != "COPY trial-keeper.sh /usr/local/bin/igdev-trial-keeper.sh" &&
			line != "COPY gateway-entrypoint.sh /usr/local/bin/igdev-gateway-entrypoint.sh" {
			t.Errorf("Dockerfile copies something other than the generated files: %q", line)
		}
	}

	seeded := sampleInput()
	seeded.APITokenHash = "c2VlZA"
	raw, err = Dockerfile(seeded)
	if err != nil {
		t.Fatalf("Dockerfile: %v", err)
	}
	if !strings.Contains(string(raw), "COPY --chown=2003:0 seed/ /usr/local/bin/ignition/data/") {
		t.Errorf("a seeded Dockerfile does not copy the seed over the data directory:\n%s", raw)
	}
}

// Materialize is the write list setup consumes: the runtime files, in the frozen
// order, named as they appear inside .igdev/runtime/, then the seed when there is
// a token to seed.
func TestMaterializeReturnsTheRuntimeFiles(t *testing.T) {
	files, err := Materialize(sampleInput())
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name)
		if len(file.Data) == 0 || !strings.HasSuffix(string(file.Data), "\n") {
			t.Errorf("%s is empty or unterminated", file.Name)
		}
	}
	if strings.Join(names, ",") != "compose.yaml,compose.env,Dockerfile,trial-keeper.sh,gateway-entrypoint.sh" {
		t.Errorf("Materialize produced %v, want compose.yaml,compose.env,Dockerfile,trial-keeper.sh,gateway-entrypoint.sh", names)
	}

	seeded := sampleInput()
	seeded.APITokenHash = "c2VlZA"
	files, err = Materialize(seeded)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	names = names[:0]
	for _, file := range files {
		names = append(names, file.Name)
	}
	want := []string{
		"compose.yaml", "compose.env", "Dockerfile", "trial-keeper.sh", "gateway-entrypoint.sh",
		"seed/config/resources/external/ignition/security-levels/config.json",
		"seed/config/resources/external/ignition/security-levels/resource.json",
		"seed/config/resources/external/ignition/security-properties/config.json",
		"seed/config/resources/external/ignition/security-properties/resource.json",
		"seed/config/resources/external/ignition/api-token/igdev/config.json",
		"seed/config/resources/external/ignition/api-token/igdev/resource.json",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Materialize produced\n%v\nwant\n%v", names, want)
	}
}

// Each staged module is mounted as one read-only file, in file-name order, so the
// image's built-in modules stay visible; with the staging cleared the Compose file
// is byte for byte what it was.
func TestComposeMountsEachStagedModuleAsAFile(t *testing.T) {
	plain, err := Compose(sampleInput())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	staged := sampleInput()
	staged.ModuleFiles = []string{"a-first.modl", "MCP Module 1.0.modl"}
	raw, err := Compose(staged)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`source: "/repo/.igdev/modules/a-first.modl"` + "\n        target: \"/usr/local/bin/ignition/user-lib/modules/a-first.modl\"\n        read_only: true",
		`source: "/repo/.igdev/modules/MCP Module 1.0.modl"`,
		`target: "/usr/local/bin/ignition/user-lib/modules/MCP Module 1.0.modl"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("compose file does not carry %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "/repo/.igdev/modules:/usr/local/bin/ignition/user-lib/modules") {
		t.Errorf("compose file still mounts the whole modules directory:\n%s", body)
	}
	cleared, err := Compose(sampleInput())
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if string(cleared) != string(plain) {
		t.Error("clearing the staging does not return the original Compose file")
	}
}
