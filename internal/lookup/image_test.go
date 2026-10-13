package lookup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadImageContainerLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "copy-failure", "extraction-failure", "remove-failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "archive.tar")
			if err := os.WriteFile(archive, tarOf(t, map[string][]byte{}), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "calls")
			t.Setenv("LOOKUP_TEST_ARCHIVE", archive)
			t.Setenv("LOOKUP_TEST_LOG", log)
			t.Setenv("LOOKUP_TEST_MODE", mode)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$LOOKUP_TEST_LOG"
case "$1" in
create) echo lookup-id ;;
cp)
  case "$LOOKUP_TEST_MODE" in
  copy-failure) echo 'copy refused' >&2; exit 1 ;;
  extraction-failure) printf 'invalid tar' ;;
  *) cat "$LOOKUP_TEST_ARCHIVE" ;;
  esac ;;
rm)
  if [ "$LOOKUP_TEST_MODE" = remove-failure ]; then
    echo 'removal refused' >&2; exit 1
  fi ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			diagnostic, err := os.CreateTemp(dir, "stderr")
			if err != nil {
				t.Fatal(err)
			}
			defer diagnostic.Close()
			previous := os.Stderr
			os.Stderr = diagnostic
			t.Cleanup(func() { os.Stderr = previous })
			got, readErr := ReadImage(Image("8.3.10"), dir)
			os.Stderr = previous
			wantError := mode == "copy-failure" || mode == "extraction-failure"
			if (readErr != nil) != wantError || len(got) != 0 {
				t.Fatalf("ReadImage = %v, %v; want error %v", got, readErr, wantError)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "create --label dev.igdev.purpose=lookup --entrypoint true inductiveautomation/ignition:8.3.10\n") {
				t.Fatalf("missing purpose label: %s", calls)
			}
			if !strings.HasSuffix(string(calls), "rm --force lookup-id\n") {
				t.Fatalf("container not removed after %s: %s", mode, calls)
			}
			warning, err := os.ReadFile(diagnostic.Name())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "remove-failure" {
				if !strings.Contains(string(warning), "removing lookup container lookup-id failed") || !strings.Contains(string(warning), "removal refused") {
					t.Fatalf("missing removal diagnostic: %s", warning)
				}
			} else if len(warning) != 0 {
				t.Fatalf("unexpected diagnostic: %s", warning)
			}
		})
	}
}
