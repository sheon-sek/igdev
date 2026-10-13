package lookup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModuleRetentionAcrossCheckouts(t *testing.T) {
	cache := t.TempDir()
	stores := []Store{NewStore(cache, "8.3.10"), NewStore(cache, "8.3.10")}
	indexes := make([]*ModuleIndex, 2)
	archives := make([]string, 2)
	for i, store := range stores {
		jar := zipOf(t, map[string][]byte{"com/acme/AcmeScripts.properties": []byte("frobnicate.desc=Frobnicates.\nfrobnicate.param.level=How hard.\n")})
		archive := zipOf(t, map[string][]byte{
			"module.xml": []byte(`<modules><module><id>com.acme.test</id><jar scope="G">acme.jar</jar></module></modules>`),
			"acme.jar":   jar, "build.txt": []byte{byte(i)},
		})
		path := filepath.Join(t.TempDir(), "acme.modl")
		if err := os.WriteFile(path, archive, 0644); err != nil {
			t.Fatal(err)
		}
		archives[i] = path
		sha, err := FileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
		key := ModuleKey(sha, nil)
		indexes[i], err = BuildModuleIndex(path, sha, key, nil, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveModule(indexes[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, store := range stores {
		if idx, ok := store.LoadModule(indexes[i].Key); !ok || len(idx.Functions) != 1 {
			t.Fatal("another checkout's save invalidated the index")
		}
	}
	old := time.Now().Add(-moduleIndexMaxAge - time.Hour)
	if err := os.Chtimes(stores[0].ModulePath(indexes[0].Key), old, old); err != nil {
		t.Fatal(err)
	}
	if err := stores[1].SaveModule(indexes[1]); err != nil {
		t.Fatal(err)
	}
	if _, ok := stores[0].LoadModule(indexes[0].Key); ok {
		t.Fatal("expired index survived")
	}
	// The checkout still owns its archive and can repopulate a cache miss.
	rebuilt, err := BuildModuleIndex(archives[0], indexes[0].SHA256, indexes[0].Key, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := stores[0].SaveModule(rebuilt); err != nil {
		t.Fatal(err)
	}
	for i, store := range stores {
		if _, ok := store.LoadModule(indexes[i].Key); !ok {
			t.Fatal("index unavailable after rebuild")
		}
	}
}

func TestModulePruneIgnoresUnownedEntriesAndFailures(t *testing.T) {
	s := NewStore(t.TempDir(), "8.3.10")
	s.pruneModules(time.Now()) // An unreadable or missing cache is best effort.
	dir := filepath.Dir(s.ModulePath("unused"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.json", "abc-def.json"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("keep"), 0644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * moduleIndexMaxAge)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s.pruneModules(time.Now())
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unowned entries changed: %v, %v", entries, err)
	}
}

func TestModulePruneDeletionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := NewStore(t.TempDir(), "8.3.10")
	key := ModuleKey("abc", nil)
	if err := s.SaveModule(&ModuleIndex{Key: key}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * moduleIndexMaxAge)
	if err := os.Chtimes(s.ModulePath(key), old, old); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.ModulePath(key))
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	s.pruneModules(time.Now())
	if _, ok := s.LoadModule(key); !ok {
		t.Fatal("index unreadable after failed deletion")
	}
}
