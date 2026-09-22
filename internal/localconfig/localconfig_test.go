package localconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".igdev", "local.toml")
}

// A generated password is long, printable, and quote-safe: it ends up inside a
// TOML string and may be pasted into a shell.
func TestGeneratePasswordIsPrintableAndRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		password, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if len(password) != PasswordLength {
			t.Fatalf("password %q has length %d, want %d", password, len(password), PasswordLength)
		}
		for _, r := range password {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Errorf("password %q carries %q, which is outside the alphabet", password, r)
			}
		}
		if seen[password] {
			t.Fatalf("GeneratePassword repeated %q", password)
		}
		seen[password] = true
	}
}

// Writing creates the tier with the credentials and the private mode.
func TestWriteCreatesPrivateTier(t *testing.T) {
	path := configPath(t)
	creds := Credentials{Username: "admin", Password: "hunter2hunter2hunter2hu"}

	changed, err := Write(path, creds)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !changed {
		t.Error("Write reported no change for a file that did not exist")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != Mode.Perm() {
		t.Errorf("mode = %o, want %o", info.Mode().Perm(), Mode.Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	loaded, err := Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != creds {
		t.Errorf("round trip = %+v, want %+v", loaded, creds)
	}
}

// A second identical write changes nothing, which is what makes re-setup
// byte-identical instead of rotating a credential the Gateway is already using.
func TestWriteIsIdempotent(t *testing.T) {
	path := configPath(t)
	creds := Credentials{Username: "admin", Password: "hunter2hunter2hunter2hu"}
	if _, err := Write(path, creds); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	changed, err := Write(path, creds)
	if err != nil {
		t.Fatalf("Write(again): %v", err)
	}
	if changed {
		t.Error("Write(again) reported a change")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("Write(again) rewrote the tier:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// igdev owns two keys, not the file: a comment, another section, or a port a
// person pinned by hand survives a write (ADR 0003 allows machine-local pins).
func TestWritePreservesEverythingItDoesNotOwn(t *testing.T) {
	path := configPath(t)
	handWritten := `# my notes
[output]
format = "text"

[gateway]
# keep the port stable for my browser bookmark
admin_username = "old"
admin_password = "stale-password-stale-pa"

[scratch]
note = "keep me"
`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	creds := Credentials{Username: "admin", Password: "freshpasswordfreshpwfresh"}
	changed, err := Write(path, creds)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !changed {
		t.Fatal("Write reported no change although the password moved")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"# my notes", `format = "text"`, "keep the port stable", `[scratch]`, `note = "keep me"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Write dropped %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "stale-password-stale-pa") {
		t.Errorf("Write left the old password in place:\n%s", body)
	}
	loaded, err := Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != creds {
		t.Errorf("credentials = %+v, want %+v", loaded, creds)
	}
	// The rewritten file is still valid TOML, so the config tier keeps resolving.
	if !parses(raw) {
		t.Errorf("Write produced content that does not parse:\n%s", body)
	}
}

// Credentials missing from a file that exists are filled in inside the existing
// [gateway] table, not appended after a later section where they would belong to
// the wrong table.
func TestWriteAddsMissingKeysToTheExistingTable(t *testing.T) {
	path := configPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[gateway]\nadmin_username = \"admin\"\n\n[scratch]\nnote = \"x\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Write(path, Credentials{Username: "admin", Password: "passwordpasswordpassword"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	loaded, err := Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Password != "passwordpasswordpassword" {
		t.Errorf("password = %q, want it recorded inside [gateway]\n%s", loaded.Password, raw)
	}
	if lines := strings.Split(string(raw), "\n"); len(lines) < 2 || lines[0] != "[gateway]" {
		t.Errorf("the [gateway] table moved:\n%s", raw)
	}
}

// A tier igdev cannot parse is replaced rather than patched: leaving broken TOML
// in place would wedge every command that resolves config.
func TestWriteReplacesUnparseableContent(t *testing.T) {
	path := configPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("this is not [ toml"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	creds := Credentials{Username: "admin", Password: "passwordpasswordpassword"}
	if _, err := Write(path, creds); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !parses(raw) {
		t.Errorf("Write left the tier unparseable:\n%s", raw)
	}
	if loaded, err := Load(raw); err != nil || loaded != creds {
		t.Errorf("credentials = %+v (%v), want %+v", loaded, err, creds)
	}
}

// A tier written in a shape the line-level merge cannot patch is replaced rather
// than left duplicated: whatever the input, the file parses and carries the
// credentials afterwards.
func TestWriteSurvivesUnmergeableShapes(t *testing.T) {
	shapes := []string{
		// An inline table: there is no [gateway] section to patch.
		"gateway = { admin_username = \"inline\", admin_password = \"old\" }\n",
		// A quoted table name, which is a different key.
		"[\"gateway\"]\nadmin_username = \"quoted\"\n",
	}
	creds := Credentials{Username: "admin", Password: "passwordpasswordpassword"}
	for _, shape := range shapes {
		t.Run(strings.SplitN(shape, "\n", 2)[0], func(t *testing.T) {
			path := configPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(shape), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := Write(path, creds); err != nil {
				t.Fatalf("Write: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !parses(raw) {
				t.Errorf("Write left the tier unparseable:\n%s", raw)
			}
			if loaded, err := Load(raw); err != nil || loaded != creds {
				t.Errorf("credentials = %+v (%v), want %+v\n%s", loaded, err, creds, raw)
			}
		})
	}
}

// An absent tier yields empty credentials and no error, which is how setup tells
// "nothing generated yet" from "this file is broken".
func TestLoadAbsentOrPartialTier(t *testing.T) {
	if creds, err := Load(nil); err != nil || creds != (Credentials{}) {
		t.Errorf("Load(nil) = %+v, %v; want empty and no error", creds, err)
	}
	creds, err := Load([]byte("[gateway]\nadmin_username = \"admin\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if creds.Username != "admin" || creds.Password != "" {
		t.Errorf("Load = %+v, want the username and no password", creds)
	}
	if _, err := Load([]byte("this is not [ toml")); err == nil {
		t.Error("Load accepted malformed TOML")
	}
}
