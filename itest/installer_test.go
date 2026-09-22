package itest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/testrig"
)

// hostArtifact is the tarball name the installer would fetch on this machine.
func hostArtifact() string {
	return "igdev-" + releaseVersion + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
}

// repoRoot is the checkout the release scripts live in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := testrig.RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	return root
}

// The one-line installer is this ticket's headline: on a fresh machine it
// detects the platform, verifies the release checksum, and leaves a working
// binary on PATH. The test serves the artifacts the packaging script really
// produced over loopback and installs into the scratch HOME's default prefix.
func TestInstallerPlacesVerifiedBinary(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	url := testrig.ServeDir(t, dist)
	env.RegisterReplacement(url, "<SERVER>")

	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url))
	if res.Exit != 0 {
		t.Fatalf("installer exited %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	installed := filepath.Join(env.Home, ".local", "bin", "igdev")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("installer did not place a binary at %s: %v\nscratch tree:\n%s",
			env.Normalize(installed), err, env.Tree())
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	// A default prefix outside PATH must be reported, or the user concludes
	// nothing happened.
	if !strings.Contains(res.Stderr, "PATH") {
		t.Errorf("installer did not tell the user to extend PATH:\n%s", res.Stderr)
	}
	env.Golden(t, "installer_output.txt", env.Normalize(res.Stdout))

	// The installed binary runs and speaks the contract.
	out, err := testrig.OutputOf(installed, "version", "--json")
	if err != nil {
		t.Fatalf("installed binary does not run: %v", err)
	}
	var envelope struct {
		Ok   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("installed binary output is not the envelope: %v\n%s", err, out)
	}
	if !envelope.Ok || envelope.Data.Version != releaseVersion {
		t.Errorf("installed binary reports %q ok=%v, want version %q",
			envelope.Data.Version, envelope.Ok, releaseVersion)
	}

	// Nothing else was left behind in the prefix or in TMPDIR.
	if entries, err := os.ReadDir(filepath.Dir(installed)); err == nil && len(entries) != 1 {
		t.Errorf("prefix holds %d entries, want only the installed binary: %v", len(entries), names(entries))
	}
	env.AssertTempDirEmpty(t)
}

// A prefix that is already on PATH is not nagged about, and pinning --version
// fetches exactly that release.
func TestInstallerHonoursPrefixAndPinnedVersion(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	prefix := env.Mkdir("prefix")
	env.SetBaseEnv("PATH=" + strings.Join(append([]string{env.Shim, prefix, testrig.GoBinDir()},
		testrig.SystemPATHList()...), string(os.PathListSeparator)))
	url := testrig.ServeDir(t, dist)

	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url)+
		" --version "+releaseVersion+" --prefix "+quote(prefix))
	if res.Exit != 0 {
		t.Fatalf("installer exited %d: %s", res.Exit, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(prefix, "igdev")); err != nil {
		t.Errorf("no binary installed in the explicit prefix: %v", err)
	}
	if strings.Contains(res.Stderr, "PATH") {
		t.Errorf("installer nagged about a prefix that is on PATH:\n%s", res.Stderr)
	}
	if !strings.Contains(res.Stdout, hostArtifact()) {
		t.Errorf("installer did not report the pinned artifact it fetched:\n%s", res.Stdout)
	}
	env.AssertTempDirEmpty(t)
}

// A tampered artifact is refused before anything is placed, and the refusal is
// loud.
func TestInstallerRefusesCorruptedArtifact(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	served := env.Mkdir("corrupt-dist")
	if res := env.RunShell("cp -r " + quote(dist+"/.") + " " + quote(served)); res.Exit != 0 {
		t.Fatalf("stage the copy: %s", res.Stderr)
	}
	artifact := filepath.Join(served, hostArtifact())
	raw, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("read staged artifact: %v", err)
	}
	if err := os.WriteFile(artifact, append(raw, []byte("tampered")...), 0o644); err != nil {
		t.Fatalf("tamper with artifact: %v", err)
	}

	prefix := env.Path("prefix")
	url := testrig.ServeDir(t, served)
	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url)+" --prefix "+quote(prefix))
	if res.Exit == 0 {
		t.Fatalf("installer accepted a tampered artifact\nstdout:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "CHECKSUM MISMATCH") {
		t.Errorf("installer did not name the failure:\n%s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(prefix, "igdev")); !os.IsNotExist(err) {
		t.Errorf("a failed verification still placed a binary")
	}
	if entries, err := os.ReadDir(prefix); err == nil && len(entries) != 0 {
		t.Errorf("the prefix was written to anyway: %v", names(entries))
	}
	env.AssertTempDirEmpty(t)
}

// An installer asked for a release that does not exist fails cleanly rather than
// installing something arbitrary.
func TestInstallerRejectsUnknownRelease(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	prefix := env.Path("prefix")
	url := testrig.ServeDir(t, dist)

	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url)+
		" --version 9.9.9 --prefix "+quote(prefix))
	if res.Exit == 0 {
		t.Fatalf("installer succeeded for a release with no artifact\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "no artifact") {
		t.Errorf("installer did not explain the miss:\n%s", res.Stderr)
	}
	if _, err := os.Stat(prefix); !os.IsNotExist(err) {
		t.Errorf("the installer created a prefix it never used")
	}
	env.AssertTempDirEmpty(t)
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
