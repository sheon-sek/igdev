package itest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/jython"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The pinned Jython checker: one verified download shared by every process on the
// machine, corrupt bytes quarantined instead of reused, and one JVM per check
// however many files the tree holds.
//
// The loopback Maven stand-in publishes a synthetic artifact, so no test reaches
// the network and no test needs a real JVM: the java shim runs the batch driver
// the CLI passes with python3, which is what makes a syntax error observable
// without one.

// fakeJarContent is the synthetic standalone artifact the loopback repository
// publishes. Its bytes are irrelevant — nothing executes them — but they are
// fixed, so the digest is.
const fakeJarContent = "igdev-synthetic-jython-standalone-2.7.4\n" +
	"a real deployment downloads the pinned upstream artifact instead\n"

// sha256Hex is the digest a golden or an override needs.
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// jythonCacheJar is where this scratch machine caches the pinned artifact.
func jythonCacheJar(env *testrig.Env) string {
	return env.CachePath(filepath.Join(jython.DirName, "2.7.4", "jython-standalone-2.7.4.jar"))
}

// jythonHarness publishes the synthetic artifact on loopback, points the run at
// it, pins its digest, and installs the java shim. It returns the counting server
// so a test can assert how many downloads happened.
func jythonHarness(t *testing.T, env *testrig.Env) *testrig.DownloadServer {
	t.Helper()
	env.Write("maven/2.7.4/jython-standalone-2.7.4.jar", fakeJarContent)
	ds := testrig.ServeDownloadDir(t, env.Path("maven"))
	env.PointAtDownload(ds)
	env.SetBaseEnv("IGDEV_JYTHON_SHA256=" + sha256Hex(fakeJarContent))
	env.ShimJava()
	return ds
}

// cacheEntries lists the Jython cache directory's file names.
func cacheEntries(t *testing.T, env *testrig.Env) []string {
	t.Helper()
	entries, err := os.ReadDir(env.CachePath(filepath.Join(jython.DirName, "2.7.4")))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the jython cache: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// assertCacheHoldsOnlyThePin fails when anything but the lock and the verified
// artifact survives in the cache directory: a temp download or an unquarantined
// partial is exactly the leak this cache must not have.
func assertCacheHoldsOnlyThePin(t *testing.T, env *testrig.Env) {
	t.Helper()
	for _, name := range cacheEntries(t, env) {
		if name == jython.LockFileName || name == "jython-standalone-2.7.4.jar" {
			continue
		}
		t.Errorf("the cache holds %s, want only the lock and the verified artifact", name)
	}
}

// One JVM compiles every file, however many the tree holds, against the one
// verified artifact the machine fetched.
func TestJythonCheckCompilesEveryFileInOneJVM(t *testing.T) {
	env := testrig.NewEnv(t)
	ds := jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/a.py", "a = 1\n")
	env.Write("repo/sub/b.py", "b = 2\n")
	env.Write("repo/sub/c.py", "c = 3\n")

	res := env.RunIn(dir, "jython", "check", ".", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	calls := env.JavaCalls(t)
	if len(calls) != 1 {
		t.Fatalf("JVM launches = %d, want exactly 1 for 3 files: %+v", len(calls), calls)
	}
	if files := calls[0].Files(); len(files) != 3 {
		t.Errorf("the JVM was given %d file(s), want 3: %v", len(files), files)
	}
	if got, want := calls[0].Jar(), jythonCacheJar(env); got != want {
		t.Errorf("the JVM ran %s, want the cached %s", got, want)
	}
	if ds.Hits() != 1 {
		t.Errorf("the artifact was downloaded %d time(s), want 1", ds.Hits())
	}

	var data struct {
		Version   string   `json:"version"`
		Jar       string   `json:"jar"`
		FileCount int      `json:"file_count"`
		Files     []string `json:"files"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Version != "2.7.4" || data.FileCount != 3 || len(data.Files) != 3 {
		t.Errorf("data = %+v, want version 2.7.4 and 3 files", data)
	}
	assertCacheHoldsOnlyThePin(t, env)
	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A clean tree exits 0 and says so.
func TestJythonCheckCleanTreePasses(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/src/alpha.py", "alpha = 1\n")
	env.Write("repo/src/beta.py", "beta = 2\n")

	res := env.RunIn(dir, "jython", "check", "src", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "jython_check_clean.json", res.Stdout)

	human := env.RunIn(dir, "jython", "check", "src")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "jython_check_clean.txt", human.Stdout)

	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A syntax error names the file and the line, and exits 1.
func TestJythonCheckReportsSyntaxErrorWithPathAndLine(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/src/ok.py", "value = 1\n")
	env.Write("repo/src/broken.py", "value = 1\nother = 2\nbroken = )\n")

	res := env.RunIn(dir, "jython", "check", "src", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJythonSyntax)

	want := filepath.Join("src", "broken.py") + ":3:"
	if !strings.Contains(envelope.Message, want) {
		t.Errorf("message %q does not name %s", envelope.Message, want)
	}
	var data struct {
		Diagnostics []jython.Diagnostic `json:"diagnostics"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if len(data.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want exactly the one broken file", data.Diagnostics)
	}
	if data.Diagnostics[0].Line != 3 || !strings.HasSuffix(data.Diagnostics[0].File, "broken.py") {
		t.Errorf("diagnostic = %+v, want broken.py line 3", data.Diagnostics[0])
	}
	env.Golden(t, "jython_check_error.json", res.Stdout)

	human := env.RunIn(dir, "jython", "check", "src")
	testrig.WantExit(t, human, contract.ExitFailure)
	env.Golden(t, "jython_check_error.txt", human.Stderr)

	res.AssertNoLeaksOutside(t, dir, env.Home, env.Path("state"))
}

// A path argument that does not exist is a fault, not a silent pass.
func TestJythonCheckMissingPathIsAFault(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := env.Mkdir("repo")

	res := env.RunIn(dir, "jython", "check", "no/such.py", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJythonPathMissing)
	if len(env.JavaCalls(t)) != 0 {
		t.Error("a missing path launched a JVM")
	}
}

// A version the embedded catalog does not pin fails closed: nothing is fetched,
// because nothing could be verified.
func TestJythonCheckRefusesAnUnpinnedVersion(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	doc := project.DefaultDoc()
	doc.Project.Name = "fixture"
	doc.Ignition.JythonVersion = "2.7.3"
	dir := env.Project("repo", string(doc.Render()))
	env.Write("repo/value.py", "value = 1\n")

	res := env.RunIn(dir, "jython", "check", "value.py", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJythonVersionUnsupported)
	if len(env.JavaCalls(t)) != 0 {
		t.Error("an unpinned version launched a JVM")
	}
	if entries := cacheEntries(t, env); len(entries) != 0 {
		t.Errorf("the cache holds %v, want nothing for an unpinned version", entries)
	}
}

// A JVM-less machine is told exactly what is missing.
func TestJythonCheckNeedsAJava(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/value.py", "value = 1\n")
	// Warm the cache so the run reaches the JVM lookup.
	testrig.WantExit(t, env.RunIn(dir, "jython", "check", "value.py"), contract.ExitOK)

	empty := env.Mkdir("empty-path")
	res := env.Run(testrig.Run{
		Args: []string{"jython", "check", "value.py", "--json"},
		Dir:  dir,
		Env:  []string{"PATH=" + empty},
	})
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJavaMissing)
}

// A mirror that refuses the request is a fetch fault, and nothing is cached.
func TestJythonCacheReportsAFetchFailure(t *testing.T) {
	env := testrig.NewEnv(t)
	ds := jythonHarness(t, env)
	ds.SetStatus(500)
	dir := env.Mkdir("repo")
	env.Write("repo/value.py", "value = 1\n")

	res := env.RunIn(dir, "jython", "check", "value.py", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeJythonFetch)
	assertCacheHoldsOnlyThePin(t, env)
	if _, err := os.Stat(jythonCacheJar(env)); err == nil {
		t.Error("a failed fetch left an artifact behind")
	}
}

// Parallel igdev processes on one machine cost one download: the per-entry lock
// serialises them, and every later process verifies what the first one stored.
func TestJythonCacheFetchesOnceUnderConcurrency(t *testing.T) {
	env := testrig.NewEnv(t)
	ds := jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/value.py", "value = 1\n")

	const runners = 6
	var (
		wait    sync.WaitGroup
		results = make([]testrig.Result, runners)
	)
	for i := range runners {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results[i] = env.Run(testrig.Run{
				Args: []string{"jython", "check", "value.py", "--json"},
				Dir:  dir,
			})
		}(i)
	}
	wait.Wait()

	for i, res := range results {
		if res.Err != nil {
			t.Fatalf("runner %d: %v", i, res.Err)
		}
		if res.Exit != int(contract.ExitOK) {
			t.Errorf("runner %d exited %d, want 0:\nstderr: %s", i, res.Exit, res.Stderr)
		}
	}
	if ds.Hits() != 1 {
		t.Errorf("the artifact was downloaded %d time(s) under %d parallel runs, want 1", ds.Hits(), runners)
	}
	if _, err := os.Stat(jythonCacheJar(env)); err != nil {
		t.Errorf("no verified artifact after the parallel runs: %v", err)
	}
	assertCacheHoldsOnlyThePin(t, env)
	env.AssertTempDirEmpty(t)
}

// A partial or corrupt entry from an interrupted run is quarantined by name and
// replaced, never reused.
func TestJythonCacheQuarantinesACorruptEntryAndRefetches(t *testing.T) {
	env := testrig.NewEnv(t)
	ds := jythonHarness(t, env)
	dir := env.Mkdir("repo")
	env.Write("repo/value.py", "value = 1\n")

	env.Write(filepath.Join("home", ".cache", "igdev", jython.DirName, "2.7.4", "jython-standalone-2.7.4.jar"), "truncated")

	testrig.WantExit(t, env.RunIn(dir, "jython", "check", "value.py"), contract.ExitOK)

	if ds.Hits() != 1 {
		t.Errorf("the artifact was downloaded %d time(s), want exactly 1 after quarantining", ds.Hits())
	}
	replaced, err := os.ReadFile(jythonCacheJar(env))
	if err != nil {
		t.Fatalf("read the refetched artifact: %v", err)
	}
	if string(replaced) != fakeJarContent {
		t.Errorf("the cache holds %q, want the refetched artifact", string(replaced))
	}
	quarantined := 0
	for _, name := range cacheEntries(t, env) {
		if !strings.HasPrefix(name, "jython-standalone-2.7.4.jar.corrupt-") {
			continue
		}
		quarantined++
		if raw, err := os.ReadFile(env.CachePath(filepath.Join(jython.DirName, "2.7.4", name))); err != nil || string(raw) != "truncated" {
			t.Errorf("quarantined %s does not hold the rejected bytes", name)
		}
	}
	if quarantined != 1 {
		t.Errorf("quarantined files = %d, want 1: %v", quarantined, cacheEntries(t, env))
	}
}

// Bytes that do not match the pin are rejected, quarantined, retried once, and
// then reported: the artifact is never used and no JVM is launched with it.
func TestJythonCacheFaultsAfterQuarantiningAMismatch(t *testing.T) {
	env := testrig.NewEnv(t)
	env.Write("maven/2.7.4/jython-standalone-2.7.4.jar", "not the pinned artifact")
	ds := testrig.ServeDownloadDir(t, env.Path("maven"))
	env.PointAtDownload(ds)
	env.SetBaseEnv("IGDEV_JYTHON_SHA256=" + sha256Hex(fakeJarContent))
	env.ShimJava()
	dir := env.Mkdir("repo")
	env.Write("repo/value.py", "value = 1\n")

	res := env.RunIn(dir, "jython", "check", "value.py", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeChecksumMismatch)

	if ds.Hits() != 2 {
		t.Errorf("downloads = %d, want 2 (the mismatch is retried once)", ds.Hits())
	}
	if len(env.JavaCalls(t)) != 0 {
		t.Error("a JVM was launched against unverified bytes")
	}
	if _, err := os.Stat(jythonCacheJar(env)); err == nil {
		t.Error("a mismatched artifact was left in the cache")
	}
	quarantined := 0
	for _, name := range cacheEntries(t, env) {
		if strings.HasPrefix(name, "jython-standalone-2.7.4.jar.corrupt-") {
			quarantined++
		}
	}
	if quarantined != 2 {
		t.Errorf("quarantined files = %d, want 2 (one per rejected download): %v", quarantined, cacheEntries(t, env))
	}
}

// The acceptance gate: 500 files, one batched JVM launch, under three seconds end
// to end. The JVM is the shim in instant mode, so what is measured is igdev's own
// orchestration — discovery, the Gate, the capability scan, the file walk, the
// cache verification, and one launch.
func TestCheckTiming500FilesUnderThreeSeconds(t *testing.T) {
	env := testrig.NewEnv(t)
	jythonHarness(t, env)
	const files = 500
	for i := range files {
		env.Write(fmt.Sprintf("repo/src/file_%03d.py", i), fmt.Sprintf("value_%03d = %d\n", i, i))
	}
	doc := project.DefaultDoc()
	doc.Project.Name = "fixture"
	doc.Scan.Jython = []string{"src"}
	doc.Scan.Capabilities = []string{"src"}
	dir := env.Project("repo", string(doc.Render()))
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)

	// Warm the cache and the checkout so the timed run pays for neither download
	// nor allocation.
	testrig.WantExit(t, env.RunIn(dir, "jython", "check", "src", "--json"), contract.ExitOK)
	before := len(env.JavaCalls(t))

	timed := env.Run(testrig.Run{
		Args: []string{"check", "--json"},
		Dir:  dir,
		Env:  []string{"IGDEV_SHIM_JAVA_FAST=1"},
	})
	testrig.WantExit(t, timed, contract.ExitOK)
	t.Logf("igdev check over %d files took %s (one JVM launch, artifact cached)", files, timed.Duration)
	if timed.Duration > 3*time.Second {
		t.Errorf("igdev check over %d files took %s, want under 3s", files, timed.Duration)
	}
	calls := env.JavaCalls(t)
	if len(calls) != before+1 {
		t.Fatalf("JVM launches = %d, want exactly one more than %d: %+v", len(calls), before, calls)
	}
	if got := len(calls[len(calls)-1].Files()); got != files {
		t.Errorf("the batched JVM was given %d file(s), want %d", got, files)
	}
}
