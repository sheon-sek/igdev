package baseline

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeBackup(t *testing.T, entries map[string]string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), FileName)
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(out)
	for name, body := range entries {
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
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

// A restored Gateway logs people in with the backup's own names, so the seed must
// name them too.
func TestSystemLoginReadsTheBackupsNames(t *testing.T) {
	file := writeBackup(t, map[string]string{
		securityPropertiesEntry: `{"systemAuthProfile":"plant-users","systemIdentityProvider":"plant_idp"}`,
	})
	userSource, provider := SystemLogin(file)
	if userSource != "plant-users" || provider != "plant_idp" {
		t.Errorf("SystemLogin = %q, %q", userSource, provider)
	}
}

// Anything unreadable, missing, or unsafe to substitute falls back to "default".
func TestSystemLoginFallsBackToDefault(t *testing.T) {
	cases := map[string]string{
		"missing file":  filepath.Join(t.TempDir(), "absent.gwbk"),
		"missing entry": writeBackup(t, map[string]string{"gateway.xml": "<x/>"}),
		"malformed":     writeBackup(t, map[string]string{securityPropertiesEntry: "{"}),
		"unsafe name":   writeBackup(t, map[string]string{securityPropertiesEntry: `{"systemAuthProfile":"a/b","systemIdentityProvider":"x\"y"}`}),
	}
	for name, file := range cases {
		userSource, provider := SystemLogin(file)
		if userSource != DefaultLoginName || provider != DefaultLoginName {
			t.Errorf("%s: SystemLogin = %q, %q, want default", name, userSource, provider)
		}
	}
}
