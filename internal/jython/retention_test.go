package jython

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
)

func TestEnsureBoundsCorruptSamplesAndPreservesVerifiedJar(t *testing.T) {
	var good atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if good.Load() {
			_, _ = w.Write([]byte("verified"))
		} else {
			_, _ = w.Write([]byte("rejected"))
		}
	}))
	defer server.Close()
	now := time.Now()
	s := Spec{Version: "2.7.4", CacheRoot: t.TempDir(), BaseURL: server.URL,
		Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("verified"))),
		Now:    func() time.Time { now = now.Add(time.Second); return now },
	}
	for range 4 {
		if _, fault := Ensure(s); fault == nil || fault.Code != contract.CodeChecksumMismatch {
			t.Fatalf("unexpected fault: %v", fault)
		}
		samples, err := filepath.Glob(s.JarPath() + ".corrupt-*")
		if err != nil || len(samples) != 2 {
			t.Fatalf("samples = %v, %v", samples, err)
		}
		for _, path := range samples {
			stamp, err := time.Parse("20060102T150405.000000000Z", path[len(s.JarPath()+".corrupt-"):])
			if err != nil || stamp.Before(now.Add(-time.Second)) {
				t.Fatal("retained an older sample")
			}
		}
	}
	good.Store(true)
	for range 2 {
		if path, fault := Ensure(s); fault != nil || path != s.JarPath() {
			t.Fatalf("verified Ensure: %s, %v", path, fault)
		}
	}
	raw, err := os.ReadFile(s.JarPath())
	if err != nil || string(raw) != "verified" {
		t.Fatalf("verified jar changed: %q, %v", raw, err)
	}
}

func TestCorruptPruneIsBestEffortAndIgnoresUnownedFiles(t *testing.T) {
	jar := filepath.Join(t.TempDir(), "jython.jar")
	pruneCorrupt(filepath.Join(jar, "missing")) // Read failure is harmless.
	for _, path := range []string{jar, jar + ".corrupt-not-a-timestamp"} {
		if err := os.WriteFile(path, []byte("keep"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(jar+".corrupt-20000101T000000.000000000Z", 0755); err != nil {
		t.Fatal(err)
	}
	pruneCorrupt(jar)
	entries, err := os.ReadDir(filepath.Dir(jar))
	if err != nil || len(entries) != 3 {
		t.Fatalf("unowned entries changed: %v, %v", entries, err)
	}
}

func TestEnsureToleratesCorruptDeletionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := Spec{Version: "2.7.4", CacheRoot: t.TempDir(), Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("verified")))}
	if err := os.MkdirAll(s.Dir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.JarPath(), []byte("verified"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), LockFileName), nil, 0644); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		name := s.JarPath() + ".corrupt-" + time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC).Format("20060102T150405.000000000Z")
		if err := os.WriteFile(name, []byte("rejected"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(s.Dir(), 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.Dir(), 0755)
	if _, fault := Ensure(s); fault != nil {
		t.Fatalf("prune failure failed Ensure: %v", fault)
	}
	samples, err := filepath.Glob(s.JarPath() + ".corrupt-*")
	if err != nil || len(samples) != 3 {
		t.Fatalf("failed deletion samples: %v, %v", samples, err)
	}
}
