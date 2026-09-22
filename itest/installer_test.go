package itest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	// A default prefix outside PATH is registered in the login profile, and the
	// installer says so where the user is reading.
	if !strings.Contains(res.Stdout, "PATH in") {
		t.Errorf("installer did not report registering PATH:\n%s", res.Stdout)
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

// A fresh machine does not have ~/.local/bin on PATH. The installer must make the
// binary reachable by name rather than only suggest it: the acceptance is
// "install script places binary on PATH, `igdev version` exits 0", which a new
// login shell proves without any help from the harness.
func TestInstallerMakesIgdevRunnableByName(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	url := testrig.ServeDir(t, dist)

	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url))
	if res.Exit != 0 {
		t.Fatalf("installer exited %d: %s", res.Exit, res.Stderr)
	}
	installed := filepath.Join(env.Home, ".local", "bin", "igdev")

	fresh, err := testrig.Command("", nil, "env", "-i", "HOME="+env.Home, "USER=igdev-test", "TERM=dumb",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"bash", "-lc", "command -v igdev && igdev version; echo exit=$?")
	if err != nil {
		t.Fatalf("start a fresh login shell: %v", err)
	}
	if fresh.Exit != 0 {
		t.Fatalf("igdev is not runnable by name in a fresh login shell:\nstdout: %s\nstderr: %s",
			fresh.Stdout, fresh.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(fresh.Stdout), "\n")
	if lines[0] != installed {
		t.Errorf("a new shell resolved igdev to %q, want %s", lines[0], env.Normalize(installed))
	}
	if !strings.Contains(fresh.Stdout, "igdev "+releaseVersion) {
		t.Errorf("fresh shell did not run the installed version:\n%s", fresh.Stdout)
	}
	if !strings.Contains(fresh.Stdout, "exit=0") {
		t.Errorf("`igdev version` did not exit 0 in a fresh shell:\n%s", fresh.Stdout)
	}
}

// Registering PATH twice would litter the profile, so the write is idempotent, and
// a prefix already on PATH is left alone.
func TestInstallerPathRegistrationIsIdempotent(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	url := testrig.ServeDir(t, dist)

	for i := range 2 {
		if res := env.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url)); res.Exit != 0 {
			t.Fatalf("install run %d failed: %s", i+1, res.Stderr)
		}
	}
	raw, err := os.ReadFile(filepath.Join(env.Home, ".profile"))
	if err != nil {
		t.Fatalf("no profile registration: %v", err)
	}
	const marker = "# igdev: install prefix on PATH"
	if got := strings.Count(string(raw), marker); got != 1 {
		t.Errorf("profile carries %d registrations, want 1:\n%s", got, raw)
	}

	other := testrig.NewEnv(t)
	prefix := other.Mkdir("bin")
	other.SetBaseEnv("PATH=" + strings.Join(append([]string{other.Shim, prefix, testrig.GoBinDir()},
		testrig.SystemPATHList()...), string(os.PathListSeparator)))
	if res := other.RunShellIn(repoRoot(t), "packaging/install.sh --base-url "+quote(url)+
		" --prefix "+quote(prefix)); res.Exit != 0 {
		t.Fatalf("installer with a PATH-visible prefix failed: %s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(other.Home, ".profile")); !os.IsNotExist(err) {
		t.Errorf("a PATH-visible prefix still rewrote a profile")
	}
}

// A pinned --version must fetch that release's own artifacts: once a newer
// release exists, the "latest" download directory no longer holds what was asked
// for, so a pinned install resolves its own tag.
func TestInstallerPinnedVersionResolvesItsOwnRelease(t *testing.T) {
	dist := packagedDist(t)
	env := testrig.NewEnv(t)
	repo := env.Mkdir("repo-base")

	latest := filepath.Join(repo, "releases", "latest", "download")
	pinned := filepath.Join(repo, "releases", "download", "v"+releaseVersion)
	for _, dir := range []string{latest, pinned} {
		if res := env.RunShell("mkdir -p " + quote(dir)); res.Exit != 0 {
			t.Fatalf("mkdir %s: %s", dir, res.Stderr)
		}
	}
	// The pinned tag holds the real artifact. The "newest" release advertises a
	// different version entirely, so picking the wrong directory is visible.
	if res := env.RunShell("cp " + quote(filepath.Join(dist, hostArtifact())) + " " + quote(pinned) +
		" && (cd " + quote(pinned) + " && sha256sum " + quote(hostArtifact()) + " > checksums.txt)"); res.Exit != 0 {
		t.Fatalf("stage the pinned release: %s", res.Stderr)
	}
	newer := "igdev-9.9.9-" + hostPlatform() + ".tar.gz"
	if res := env.RunShell("cp " + quote(filepath.Join(dist, hostArtifact())) + " " +
		quote(filepath.Join(latest, newer)) + " && (cd " + quote(latest) +
		" && sha256sum " + quote(newer) + " > checksums.txt)"); res.Exit != 0 {
		t.Fatalf("stage the newest release: %s", res.Stderr)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(repo)))
	t.Cleanup(server.Close)

	prefix := env.Path("prefix")
	res := env.RunShellIn(repoRoot(t), "packaging/install.sh --repo-url "+quote(server.URL)+
		" --version "+releaseVersion+" --prefix "+quote(prefix))
	if res.Exit != 0 {
		t.Fatalf("pinned install exited %d\nstdout:\n%s\nstderr:\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, hostArtifact()) {
		t.Errorf("pinned install did not fetch the pinned artifact:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, newer) {
		t.Errorf("pinned install fetched the newest release instead:\n%s", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(prefix, "igdev")); err != nil {
		t.Errorf("pinned install placed no binary: %v", err)
	}

	// With nothing pinned, the newest release is the one that answers.
	unpinned := env.RunShellIn(repoRoot(t), "packaging/install.sh --repo-url "+quote(server.URL)+
		" --prefix "+quote(env.Path("prefix2")))
	if unpinned.Exit != 0 {
		t.Fatalf("unpinned install exited %d: %s", unpinned.Exit, unpinned.Stderr)
	}
	if !strings.Contains(unpinned.Stdout, newer) {
		t.Errorf("unpinned install did not use the latest release:\n%s", unpinned.Stdout)
	}
}

// A version token is spliced into a URL and a filename, so anything but a
// semver-ish string is refused before a request is made.
func TestInstallerRejectsMalformedVersion(t *testing.T) {
	env := testrig.NewEnv(t)
	for _, bad := range []string{".*", "a b", "../elsewhere", "v1/2", "*"} {
		res := env.RunShellIn(repoRoot(t), "packaging/install.sh --version "+quote(bad)+
			" --prefix "+quote(env.Path("prefix"))+" --base-url http://127.0.0.1:1/none")
		if res.Exit == 0 {
			t.Errorf("--version %q was accepted", bad)
			continue
		}
		if !strings.Contains(res.Stderr, "version") {
			t.Errorf("--version %q rejected without naming the problem:\n%s", bad, res.Stderr)
		}
	}
	if _, err := os.Stat(env.Path("prefix")); !os.IsNotExist(err) {
		t.Errorf("a rejected version still created the install prefix")
	}
}

func hostPlatform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
