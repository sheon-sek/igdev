package projectzip

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The same tree zips to the same bytes, and unpacking it gives the tree back.
func TestZipIsDeterministicAndRoundTrips(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"project.json":                           `{"title":"demo"}`,
		"ignition/script-python/a/code.py":       "x = 1\n",
		"ignition/script-python/a/resource.json": "{}",
	}
	write(t, src, files)
	first, err := Zip(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(src, "project.json"), epoch.AddDate(5, 0, 0), epoch.AddDate(5, 0, 0)); err != nil {
		t.Fatal(err)
	}
	second, _ := Zip(src)
	if !bytes.Equal(first, second) {
		t.Error("zipping the same tree twice gave different bytes")
	}

	dst := filepath.Join(t.TempDir(), "out")
	names, err := Unpack(first, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(files) {
		t.Errorf("unpacked %v", names)
	}
	for name, body := range files {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil || string(got) != body {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
}

// Unpacking over a project directory removes the files the export no longer
// carries; a directory that is not a project is never cleared.
func TestUnpackMirrorsAProjectAndRefusesOtherDirectories(t *testing.T) {
	src := t.TempDir()
	write(t, src, map[string]string{"project.json": "{}", "keep.txt": "k"})
	data, err := Zip(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	write(t, dst, map[string]string{"project.json": "old", "stale/gone.txt": "x"})
	if _, err := Unpack(data, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale")); !os.IsNotExist(err) {
		t.Error("a file the export does not carry survived")
	}
	other := t.TempDir()
	write(t, other, map[string]string{"notes.txt": "mine"})
	if _, err := Unpack(data, other); err == nil {
		t.Error("unpacked over a directory that is not a project")
	}
}

func TestZipAndCheckRefuseWhatIsNotAProject(t *testing.T) {
	if _, err := Zip(t.TempDir()); err == nil {
		t.Error("zipped a directory without project.json")
	}
	if err := Check([]byte("not a zip")); err == nil {
		t.Error("accepted bytes that are not a zip")
	}
	for _, name := range []string{"../evil.txt", "/abs.txt", "a/../../b"} {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		for _, n := range []string{"project.json", name} {
			f, _ := w.Create(n)
			_, _ = f.Write([]byte("{}"))
		}
		_ = w.Close()
		if err := Check(buf.Bytes()); err == nil {
			t.Errorf("accepted the entry %q", name)
		}
	}
}
