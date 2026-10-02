package projectseed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tagProvider = `{"profile":{"type":"STANDARD"},"settings":{"readOnly":false}}`

func wantRefusal(t *testing.T, err error, fragments ...string) {
	t.Helper()
	fault, ok := err.(*contract.Fault)
	if !ok || fault.Code != contract.CodeConfigInvalid {
		t.Fatalf("Load = %v, want IGDEV_E_CONFIG_INVALID", err)
	}
	for _, f := range fragments {
		if !strings.Contains(fault.Message, f) {
			t.Errorf("message %q does not name %q", fault.Message, f)
		}
	}
}

// An allowed resource loads, in path order, with a digest that follows its bytes.
func TestLoadAcceptsAnAllowedResource(t *testing.T) {
	root := t.TempDir()
	write(t, root, "seed/ignition/tag-provider/sim/config.json", tagProvider)
	write(t, root, "seed/ignition/tag-provider/sim/resource.json", `{"scope":"A"}`)
	write(t, root, "more/com.inductiveautomation.opcua/device/plc/config.json", `{"settings":{"password":""}}`)

	seed, err := Load(root, []string{"seed", "more"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var paths []string
	for _, f := range seed.Files {
		paths = append(paths, f.Path)
	}
	want := "com.inductiveautomation.opcua/device/plc/config.json,ignition/tag-provider/sim/config.json,ignition/tag-provider/sim/resource.json"
	if strings.Join(paths, ",") != want {
		t.Errorf("files = %v, want %s", paths, want)
	}
	again, _ := Load(root, []string{"more", "seed"})
	if again.Digest != seed.Digest || seed.Digest == "" {
		t.Errorf("the digest depends on directory order or is empty: %q vs %q", seed.Digest, again.Digest)
	}
	write(t, root, "seed/ignition/tag-provider/sim/config.json", `{"settings":{"readOnly":true}}`)
	if Digest(root, []string{"seed", "more"}) == seed.Digest {
		t.Error("changing a seed file does not change the digest")
	}
}

func TestLoadRefusesAnUnlistedType(t *testing.T) {
	root := t.TempDir()
	write(t, root, "seed/ignition/user-source/people/config.json", `{}`)
	_, err := Load(root, []string{"seed"})
	wantRefusal(t, err, "seed/ignition/user-source/people/config.json", "not on the seed allowlist")
}

func TestLoadRefusesIgdevsOwnResources(t *testing.T) {
	root := t.TempDir()
	write(t, root, "seed/ignition/security-properties/config.json", `{}`)
	write(t, root, "seed/ignition/security-properties/x/config.json", `{}`)
	_, err := Load(root, []string{"seed"})
	wantRefusal(t, err, "collides with igdev's own ignition/security-properties")
}

func TestLoadRefusesTwoDirectoriesWritingOneFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/ignition/tag-provider/sim/config.json", tagProvider)
	write(t, root, "b/ignition/tag-provider/sim/config.json", tagProvider)
	_, err := Load(root, []string{"a", "b"})
	wantRefusal(t, err, "b/ignition/tag-provider/sim/config.json", "collides with a/ignition/tag-provider/sim/config.json")
}

func TestLoadRefusesASecret(t *testing.T) {
	root := t.TempDir()
	write(t, root, "seed/ignition/database-connection/hist/config.json",
		`{"settings":{"connectURL":"jdbc:postgresql://db/hist","auth":{"Password":"hunter2"}}}`)
	_, err := Load(root, []string{"seed"})
	wantRefusal(t, err, "seed/ignition/database-connection/hist/config.json", ".settings.auth.Password")

	write(t, root, "seed/ignition/database-connection/hist/config.json",
		`{"settings":{"auth":{"password":{"type":"Embedded","data":{"ciphertext":"x"}}}}}`)
	_, err = Load(root, []string{"seed"})
	wantRefusal(t, err, ".settings.auth.password")
}

func TestLoadRefusesASymlinkAndAMissingDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "outside.json", `{}`)
	if err := os.MkdirAll(filepath.Join(root, "seed/ignition/tag-provider/sim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.json"), filepath.Join(root, "seed/ignition/tag-provider/sim/config.json")); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root, []string{"seed"})
	wantRefusal(t, err, "is a symlink")
	_, err = Load(root, []string{"nope"})
	wantRefusal(t, err, `"nope" is not a directory`)
}
