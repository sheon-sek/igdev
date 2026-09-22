package testrig

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

// Binary compiles cmd/igdev once per test process and returns the path to it.
// Building the real binary is the point of seam S1: the tests assert what the
// installed program does, not what a package function returns.
//
// The build mirrors the release build (CGO off, stripped, trimmed paths) so the
// binary-size gate measures a release-shaped artifact.
func Binary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() { binaryPath, binaryErr = buildBinary() })
	if binaryErr != nil {
		t.Fatalf("build igdev for the test rig: %v", binaryErr)
	}
	return binaryPath
}

// BinarySize returns the size in bytes of the binary under test.
func BinarySize(t *testing.T) int64 {
	t.Helper()
	path := Binary(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// CleanupBinary removes the scratch directory holding the built binary; call it
// from TestMain.
func CleanupBinary() {
	if binaryPath == "" {
		return
	}
	_ = os.RemoveAll(filepath.Dir(binaryPath))
	binaryPath = ""
}

func buildBinary() (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "igdev-rig-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "igdev")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/igdev")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOFLAGS=")
	if raw, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("go build: %w\n%s", err, raw)
	}
	return out, nil
}

// RepoRoot locates the module root by walking up from the test's working
// directory, so a test can run `go build` from anywhere in the tree.
func RepoRoot() (string, error) {
	here, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := here; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", here)
		}
		dir = parent
	}
}
