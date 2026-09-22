package itest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// releaseVersion is the version the packaging test stamps, chosen so it can
// never be confused with a real release.
const releaseVersion = "0.0.0-package-test"

var (
	distOnce sync.Once
	distDir  string
	distErr  error
)

// packagedDist runs the real packaging script once per test process and returns
// the directory it produced. Serving that tree is what lets the installer test
// exercise the actual release layout instead of a hand-made imitation.
func packagedDist(t *testing.T) string {
	t.Helper()
	distOnce.Do(func() { distDir, distErr = buildDist() })
	if distErr != nil {
		t.Fatalf("package igdev for the release tests: %v", distErr)
	}
	return distDir
}

func buildDist() (string, error) {
	root, err := testrig.RepoRoot()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "igdev-dist-")
	if err != nil {
		return "", err
	}
	err = testrig.RunScriptIn(root, []string{"IGDEV_DIST_DIR=" + dir}, "packaging/package.sh", releaseVersion)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return filepath.Join(dir, releaseVersion), nil
}

// CleanupDist removes the packaged tree; TestMain calls it.
func CleanupDist() {
	if distDir != "" {
		os.RemoveAll(filepath.Dir(distDir))
		distDir = ""
	}
}

// The packaging script produces one tarball per supported target plus a
// checksums file, and the stamped binary reports the version it was built for.
func TestPackageScriptBuildsReleaseArtifacts(t *testing.T) {
	dist := packagedDist(t)

	checksums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		t.Fatalf("no checksums.txt in %s: %v", dist, err)
	}
	lines := strings.Split(strings.TrimRight(string(checksums), "\n"), "\n")
	pattern := regexp.MustCompile(`^[0-9a-f]{64}  igdev-` + regexp.QuoteMeta(releaseVersion) +
		`-(linux|darwin)-(amd64|arm64)\.tar\.gz$`)
	// Every target the installer accepts ships: both OSes against both
	// architectures (ADR 0002 keeps Windows out).
	targets := map[string]bool{
		"linux-amd64": false, "linux-arm64": false,
		"darwin-amd64": false, "darwin-arm64": false,
	}
	for _, line := range lines {
		if !pattern.MatchString(line) {
			t.Errorf("checksums.txt line is not `<sha256>  <artifact>`: %q", line)
			continue
		}
		name := strings.SplitN(line, "  ", 2)[1]
		for target := range targets {
			if strings.HasSuffix(name, target+".tar.gz") {
				targets[target] = true
			}
		}
	}
	for target, found := range targets {
		if !found {
			t.Errorf("no %s artifact in checksums.txt", target)
		}
	}

	// The release contract is that `shasum -c`/`sha256sum -c` verifies the tree.
	env := testrig.NewEnv(t)
	res := env.RunShellIn(dist, "sha256sum -c --status checksums.txt && echo VERIFIED")
	if res.Exit != 0 || !strings.Contains(res.Stdout, "VERIFIED") {
		t.Errorf("checksums.txt does not verify its own artifacts: exit %d stdout %q stderr %q",
			res.Exit, res.Stdout, res.Stderr)
	}

	// The tarball for this host holds a runnable, version-stamped binary in one
	// top-level directory named after the artifact.
	artifact := filepath.Join(dist, hostArtifact())
	unpacked := strings.TrimSuffix(hostArtifact(), ".tar.gz")
	out := t.TempDir()
	if res := env.RunShellIn(out, "tar -xzf "+quote(artifact)); res.Exit != 0 {
		t.Fatalf("unpack %s: %s", artifact, res.Stderr)
	}
	stamped := filepath.Join(out, unpacked, "igdev")
	raw, err := os.ReadFile(stamped)
	if err != nil {
		t.Fatalf("packed binary missing: %v\nscratch tree:\n%s", err, env.Tree())
	}
	if len(raw) < 1<<20 {
		t.Errorf("packed binary is %d bytes, too small to be a real build", len(raw))
	}
	versionJSON, err := testrig.OutputOf(stamped, "version", "--json")
	if err != nil {
		t.Fatalf("run the packaged binary: %v", err)
	}
	var envelope struct {
		Ok   bool `json:"ok"`
		Data struct {
			Version  string `json:"version"`
			Contract string `json:"contract"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(versionJSON), &envelope); err != nil {
		t.Fatalf("packaged binary output is not the envelope: %v\n%s", err, versionJSON)
	}
	if !envelope.Ok || envelope.Data.Version != releaseVersion {
		t.Errorf("packaged binary reports version %q ok=%v, want %q",
			envelope.Data.Version, envelope.Ok, releaseVersion)
	}
	if envelope.Data.Contract != contract.Version {
		t.Errorf("packaged binary reports contract %q, want %q", envelope.Data.Contract, contract.Version)
	}
}
