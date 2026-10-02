package runtimeassets

import (
	"encoding/json"
	"strings"
	"testing"
)

func seededInput() Input {
	in := sampleInput()
	in.APITokenHash = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"
	in.TrialAutoReset = true
	return in
}

func seedFile(t *testing.T, in Input, name string) map[string]any {
	t.Helper()
	files, err := Materialize(in)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	for _, file := range files {
		if file.Name == "seed/config/resources/external/ignition/"+name {
			var out map[string]any
			if err := json.Unmarshal(file.Data, &out); err != nil {
				t.Fatalf("%s is not JSON: %v", name, err)
			}
			return out
		}
	}
	t.Fatalf("Materialize rendered no %s", name)
	return nil
}

// The token resource carries the hash setup computed, under the igdev level, and
// nothing else that could authenticate.
func TestSeedCarriesTheTokenHashOnly(t *testing.T) {
	in := seededInput()
	config := seedFile(t, in, "api-token/igdev/config.json")
	settings := config["settings"].(map[string]any)
	if settings["tokenHash"] != in.APITokenHash {
		t.Errorf("tokenHash = %v, want the rendered hash", settings["tokenHash"])
	}
	profile, _ := json.Marshal(config["profile"])
	if !strings.Contains(string(profile), `"name":"IgdevAdmin"`) {
		t.Errorf("the token does not hold the igdev level: %s", profile)
	}
}

// The general security settings are what grant the token its rights, so they must
// not be overridable: the Gateway's first start writes defaults into the core
// collection that would otherwise shadow them. Administrator keeps every right it
// had.
func TestSeedSecurityPropertiesGrantTheTokenAndKeepAdministrator(t *testing.T) {
	in := seededInput()
	meta := seedFile(t, in, "security-properties/resource.json")
	if meta["overridable"] != false {
		t.Errorf("security-properties is overridable: %v", meta)
	}
	props := seedFile(t, in, "security-properties/config.json")
	for _, key := range []string{"readPermissions", "writePermissions", "designerPermissions"} {
		raw, _ := json.Marshal(props[key])
		for _, level := range []string{`"name":"Administrator"`, `"name":"IgdevAdmin"`} {
			if !strings.Contains(string(raw), level) {
				t.Errorf("%s does not list %s: %s", key, level, raw)
			}
		}
	}
	levels := seedFile(t, in, "security-levels/config.json")
	raw, _ := json.Marshal(levels)
	if !strings.Contains(string(raw), `"name":"IgdevAdmin"`) || !strings.Contains(string(raw), `"name":"Administrator"`) {
		t.Errorf("security-levels does not declare both levels: %s", raw)
	}
}

// Seed files are a pure function of the input: the resource uuids derive from the
// Instance identity, so a second setup re-materializes nothing.
func TestSeedIsDeterministic(t *testing.T) {
	first, err := Materialize(seededInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Materialize(seededInput())
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if string(first[i].Data) != string(second[i].Data) {
			t.Errorf("%s differs between two renders", first[i].Name)
		}
	}
	other := seededInput()
	other.InstanceID = "00000000-0000-4000-8000-000000000000"
	a := seedFile(t, seededInput(), "api-token/igdev/resource.json")["attributes"].(map[string]any)["uuid"]
	b := seedFile(t, other, "api-token/igdev/resource.json")["attributes"].(map[string]any)["uuid"]
	if a == b {
		t.Errorf("two Instances render the same resource uuid %v", a)
	}
}

// The trial keeper runs only when the contract asks for it and there is a token to
// reset with, and the Compose file references the token instead of carrying it.
func TestComposeRunsTheTrialKeeperWhenAsked(t *testing.T) {
	raw, err := Compose(seededInput())
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"  trial-keeper:\n",
		"image: igdev-3b1f0c2a:8.3.8",
		"pull_policy: never",
		`entrypoint: ["bash", "/usr/local/bin/igdev-trial-keeper.sh"]`,
		"IGDEV_GATEWAY_API_TOKEN: ${IGDEV_GATEWAY_API_TOKEN:-}",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("compose.yaml does not carry %q:\n%s", want, body)
		}
	}

	off := seededInput()
	off.TrialAutoReset = false
	unseeded := sampleInput()
	unseeded.TrialAutoReset = true
	for name, in := range map[string]Input{"trial_reset off": off, "no token": unseeded} {
		raw, err := Compose(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "trial-keeper") {
			t.Errorf("%s: compose.yaml runs the trial keeper:\n%s", name, raw)
		}
	}
}

// The keeper resets only an expired trial, with the headers the Gateway's web UI
// sends, and never names a token value.
func TestTrialKeeperResetsOnlyAnExpiredTrial(t *testing.T) {
	files, err := Materialize(seededInput())
	if err != nil {
		t.Fatal(err)
	}
	body := string(files[3].Data)
	if files[3].Name != KeeperFileName {
		t.Fatalf("the fourth runtime file is %s", files[3].Name)
	}
	for _, want := range []string{
		`"expired"[[:space:]]*:[[:space:]]*true`,
		"-X POST",
		`-H "Origin: ${url}"`,
		`-H "Referer: ${url}/app/home"`,
		`-H "X-Ignition-API-Token: ${token}"`,
		`token="${IGDEV_GATEWAY_API_TOKEN:-}"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("trial-keeper.sh does not carry %q", want)
		}
	}
	if strings.Contains(body, seededInput().APITokenHash) {
		t.Error("trial-keeper.sh carries the token hash")
	}
}
