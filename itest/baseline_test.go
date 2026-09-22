package itest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/baseline"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The Baseline: a .gwbk staged into the Checkout Setup that the next fresh
// Gateway launch restores from. These tests assert the whole observable
// behaviour through seam S1 — what `set` stages, what `status` reports, what
// `clear` removes, and what the container engine is handed — plus the rule that
// a Baseline is checkout-local user state which re-running `setup` never
// discards.

// baselineFixture materializes one Instance and returns the Project Root: a
// Baseline lives in the Checkout Setup, so every Baseline verb needs one.
func baselineFixture(t *testing.T, env *testrig.Env) string {
	t.Helper()
	dir := env.Project("repo", testrig.MinimalContract)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	return dir
}

// baselineDir is where this fixture's Checkout Setup holds the Baseline.
func baselineDir(dir string) string {
	return baseline.Dir(filepath.Join(dir, project.StateDir))
}

// backupSource writes a stand-in .gwbk inside the scratch tree and registers its
// path so a golden can carry a literal token for it.
func backupSource(t *testing.T, env *testrig.Env, rel, body, token string) string {
	t.Helper()
	path := env.Write(rel, body)
	env.RegisterReplacement(path, token)
	return path
}

// baselineState is the data member of a baseline set/status envelope.
type baselineState struct {
	Staged      bool   `json:"staged"`
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
	StagedAt    string `json:"staged_at"`
	RestoreArgs string `json:"restore_args"`
}

// baselineData decodes the data member of a baseline set/status envelope.
func baselineData(t *testing.T, stdout string) baselineState {
	t.Helper()
	var data baselineState
	testrig.DataOf(t, stdout, &data)
	return data
}

// sha256Of is the digest the CLI has to report for a staged file.
func sha256Of(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// normalizeStagedAt makes the staging timestamp collapse to a token, the way the
// Consent timestamps do.
func normalizeStagedAt(t *testing.T, env *testrig.Env, dir string) {
	t.Helper()
	state := baseline.Read(baselineDir(dir))
	if state.StagedAt != "" {
		env.RegisterReplacement(state.StagedAt, "<STAGED_AT>")
	}
}

// set stages a source file and fails the test unless it succeeded.
func stage(t *testing.T, env *testrig.Env, dir, source string) testrig.Result {
	t.Helper()
	res := env.RunIn(dir, "baseline", "set", source)
	testrig.WantExit(t, res, contract.ExitOK)
	return res
}

// set copies the source into the Checkout Setup, reports where it came from and
// what it is, and the staged copy stands alone: the source may disappear and the
// Gateway still restores from the staged file.
func TestBaselineSetStagesTheBackup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	const body = "gateway backup bytes\n"
	source := backupSource(t, env, "backups/customer.gwbk", body, "<SOURCE>")

	res := env.RunIn(dir, "baseline", "set", source, "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	normalizeStagedAt(t, env, dir)
	env.Golden(t, "baseline_set.json", res.Stdout)

	data := baselineData(t, res.Stdout)
	staged := baseline.FilePath(baselineDir(dir))
	if !data.Staged || data.Path != staged {
		t.Errorf("set reported %+v, want the staged %s", data, staged)
	}
	if data.Source != source {
		t.Errorf("set reported source %q, want %q", data.Source, source)
	}
	if data.Bytes != int64(len(body)) {
		t.Errorf("set reported %d bytes, want %d", data.Bytes, len(body))
	}
	if data.SHA256 != sha256Of(body) {
		t.Errorf("set reported sha256 %q, want %q", data.SHA256, sha256Of(body))
	}
	if data.StagedAt == "" {
		t.Error("set reported no staged_at")
	}
	if data.RestoreArgs != baseline.Args() {
		t.Errorf("set reported restore_args %q, want %q", data.RestoreArgs, baseline.Args())
	}

	stagedBody, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read the staged Baseline: %v", err)
	}
	if string(stagedBody) != body {
		t.Errorf("staged bytes = %q, want the source's %q", stagedBody, body)
	}
	// The Baseline is a copy, not a reference: the machine that staged it can
	// lose the original and the Gateway still restores from the staged file.
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove the source: %v", err)
	}
	after := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, after, contract.ExitOK)
	if status := baselineData(t, after.Stdout); !status.Staged {
		t.Errorf("the staged Baseline stopped being staged when its source was removed: %+v", status)
	}

	// Human mode reports the same staging as prose.
	human := stage(t, env, dir, backupSource(t, env, "backups/other.gwbk", body, "<SOURCE_OTHER>"))
	normalizeStagedAt(t, env, dir)
	env.Golden(t, "baseline_set.txt", human.Stdout)
	env.AssertNoDockerCalls(t)
}

// status reports staged-or-empty, and never touches the container engine: it is
// a read of checkout-local state.
func TestBaselineStatusReportsStagedOrEmpty(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)

	empty := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, empty, contract.ExitOK)
	env.Golden(t, "baseline_status_empty.json", empty.Stdout)
	if data := baselineData(t, empty.Stdout); data.Staged || data.Path != "" || data.Source != "" {
		t.Errorf("an empty Baseline directory reported %+v, want staged false", data)
	}
	emptyHuman := env.RunIn(dir, "baseline", "status")
	testrig.WantExit(t, emptyHuman, contract.ExitOK)
	env.Golden(t, "baseline_status_empty.txt", emptyHuman.Stdout)

	const body = "gateway backup bytes\n"
	source := backupSource(t, env, "backups/customer.gwbk", body, "<SOURCE>")
	stage(t, env, dir, source)
	// The staged copy is the one this run wrote, so its timestamp is the run's.
	normalizeStagedAt(t, env, dir)

	staged := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, staged, contract.ExitOK)
	env.Golden(t, "baseline_status_staged.json", staged.Stdout)
	data := baselineData(t, staged.Stdout)
	if !data.Staged || data.Source != source || data.SHA256 != sha256Of(body) {
		t.Errorf("status reported %+v, want the staged %s", data, source)
	}

	human := env.RunIn(dir, "baseline", "status")
	testrig.WantExit(t, human, contract.ExitOK)
	env.Golden(t, "baseline_status_staged.txt", human.Stdout)

	// A staged file replaced behind igdev's back is still staged — the file is
	// the state — but the recorded provenance no longer describes it, so status
	// reports the file without it rather than a source that is not true.
	if err := os.WriteFile(baseline.FilePath(baselineDir(dir)), []byte("someone else's backup\n"), 0o644); err != nil {
		t.Fatalf("replace the staged file: %v", err)
	}
	replaced := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, replaced, contract.ExitOK)
	after := baselineData(t, replaced.Stdout)
	if !after.Staged || after.Path == "" {
		t.Errorf("status reported %+v, want the replaced file still staged", after)
	}
	if after.Source != "" || after.SHA256 != "" {
		t.Errorf("status reported stale provenance for a replaced file: %+v", after)
	}
	env.AssertNoDockerCalls(t)
}

// clear removes the staged backup and its record, leaves the mount point in
// place, and is a successful no-op when nothing is staged.
func TestBaselineClearRemovesTheStagedBackup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)

	const body = "gateway backup bytes\n"
	source := backupSource(t, env, "backups/customer.gwbk", body, "<SOURCE>")
	stage(t, env, dir, source)

	cleared := env.RunIn(dir, "baseline", "clear", "--json")
	testrig.WantExit(t, cleared, contract.ExitOK)
	env.Golden(t, "baseline_clear.json", cleared.Stdout)

	var removed struct {
		Removed []string `json:"removed"`
	}
	testrig.DataOf(t, cleared.Stdout, &removed)
	want := []string{baseline.FilePath(baselineDir(dir)), baseline.RecordPath(baselineDir(dir))}
	if strings.Join(removed.Removed, ",") != strings.Join(want, ",") {
		t.Errorf("clear removed %v, want %v", removed.Removed, want)
	}
	for _, path := range want {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists after clear (err %v)", path, err)
		}
	}
	// The mount point stays: the Compose file mounts this directory, and a
	// Gateway starting against a missing path would have it created by the engine.
	if info, err := os.Stat(baselineDir(dir)); err != nil || !info.IsDir() {
		t.Errorf("clear removed the Baseline directory (err %v)", err)
	}

	status := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, status, contract.ExitOK)
	if data := baselineData(t, status.Stdout); data.Staged {
		t.Errorf("the Baseline is still staged after clear: %+v", data)
	}

	// Clearing an empty Baseline is a successful no-op, not a fault.
	again := env.RunIn(dir, "baseline", "clear", "--json")
	testrig.WantExit(t, again, contract.ExitOK)
	var empty struct {
		Removed []string `json:"removed"`
	}
	testrig.DataOf(t, again.Stdout, &empty)
	if len(empty.Removed) != 0 {
		t.Errorf("a second clear removed %v, want nothing", empty.Removed)
	}
	env.AssertNoDockerCalls(t)
}

// Staging a second time replaces: it is checkout-local user state, so there is
// no contract to diff and no prompt — the newest staged file is the one the next
// `gateway up` restores from.
func TestBaselineSetReplacesTheStagedBackup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	first := backupSource(t, env, "backups/first.gwbk", "first backup\n", "<SOURCE_FIRST>")
	second := backupSource(t, env, "backups/second.gwbk", "second backup, longer\n", "<SOURCE_SECOND>")

	stage(t, env, dir, first)
	stage(t, env, dir, second)

	status := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, status, contract.ExitOK)
	data := baselineData(t, status.Stdout)
	if data.Source != second || data.SHA256 != sha256Of("second backup, longer\n") {
		t.Errorf("status reported %+v, want the second source", data)
	}
	body, err := os.ReadFile(baseline.FilePath(baselineDir(dir)))
	if err != nil {
		t.Fatalf("read the staged Baseline: %v", err)
	}
	if string(body) != "second backup, longer\n" {
		t.Errorf("staged bytes = %q, want the second source's", body)
	}
	env.AssertNoDockerCalls(t)
}

// A source that is not there fails cleanly, naming the path and the command that
// would work.
func TestBaselineMissingFileIsANamedFault(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	source := env.Path("backups/absent.gwbk")
	env.RegisterReplacement(source, "<SOURCE>")

	res := env.RunIn(dir, "baseline", "set", source, "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	env.Golden(t, "baseline_missing.json", res.Stdout)
	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeBaselineMissing)
	if !strings.Contains(envelope.Message, source) {
		t.Errorf("the message does not name the missing path: %q", envelope.Message)
	}
	testrig.WantRemediation(t, envelope, "igdev baseline set <file.gwbk>")

	// A refused set stages nothing.
	for _, path := range []string{
		baseline.FilePath(baselineDir(dir)),
		baseline.RecordPath(baselineDir(dir)),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a refused set created %s (err %v)", path, err)
		}
	}
	res.AssertNoLeaks(t)
	env.AssertNoDockerCalls(t)
}

// Anything that is not an existing .gwbk is refused: a wrong extension is a
// usage error, and a path that exists but is not a readable file is a named
// fault.
func TestBaselineRejectsWhatIsNotABackup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)

	t.Run("wrong extension", func(t *testing.T) {
		source := backupSource(t, env, "backups/customer.tar", "not a backup\n", "<SOURCE>")
		res := env.RunIn(dir, "baseline", "set", source, "--json")
		testrig.WantExit(t, res, contract.ExitUsage)
		env.Golden(t, "baseline_wrong_extension.json", res.Stdout)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeUsage)
		if !strings.Contains(envelope.Message, ".gwbk") {
			t.Errorf("the message does not name the extension: %q", envelope.Message)
		}
	})

	t.Run("directory", func(t *testing.T) {
		source := env.Mkdir("backups/not-a-file.gwbk")
		env.RegisterReplacement(source, "<SOURCE_DIR>")
		res := env.RunIn(dir, "baseline", "set", source, "--json")
		testrig.WantExit(t, res, contract.ExitFailure)
		envelope := testrig.Envelope(t, res.Stdout)
		testrig.WantCode(t, envelope, contract.CodeBaselineInvalid)
	})

	if _, err := os.Stat(baseline.FilePath(baselineDir(dir))); !os.IsNotExist(err) {
		t.Errorf("a refused set staged something (err %v)", err)
	}
	env.AssertNoDockerCalls(t)
}

// The Baseline lives in the Checkout Setup, so a checkout that was never
// materialized has none to report: the Gate refuses first and names the repair.
func TestBaselineRequiresSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Project("repo", testrig.MinimalContract)

	for _, args := range [][]string{
		{"baseline", "set", env.Path("backups/customer.gwbk")},
		{"baseline", "status"},
		{"baseline", "clear"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := env.RunIn(dir, append(args, "--json")...)
			testrig.WantExit(t, res, contract.ExitFailure)
			envelope := testrig.Envelope(t, res.Stdout)
			testrig.WantCode(t, envelope, contract.CodeSetupRequired)
			testrig.WantRemediation(t, envelope, "igdev setup")
			res.AssertNoLeaks(t)
		})
	}
	env.AssertNoDockerCalls(t)
}

// A Baseline is checkout-local user state: re-running setup — including the
// setup that clears a stale stamp after a contract edit — never discards it, and
// nothing setup materializes owns the staged file.
func TestBaselineSurvivesSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	const body = "gateway backup bytes\n"
	source := backupSource(t, env, "backups/customer.gwbk", body, "<SOURCE>")
	stage(t, env, dir, source)

	again := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, again, contract.ExitOK)
	assertStaged(t, env, dir, source, body)
	for _, write := range materializedPaths(t, again.Stdout) {
		if write == baseline.FilePath(baselineDir(dir)) || write == baseline.RecordPath(baselineDir(dir)) {
			t.Errorf("setup reported the staged Baseline as its own: %s", write)
		}
	}

	// A hand-edited contract makes the checkout stale; the Baseline is still
	// there, and the setup that repairs the stamp keeps it.
	env.Write("repo/igdev.toml", testrig.MinimalContract+"# hand-edited\n")
	stale := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, stale, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, stale.Stdout), contract.CodeSetupStale)

	testrig.WantExit(t, env.RunIn(dir, "setup"), contract.ExitOK)
	assertStaged(t, env, dir, source, body)
	env.AssertNoDockerCalls(t)
}

// assertStaged fails unless the Baseline the fixture staged is still staged with
// the bytes it was staged from.
func assertStaged(t *testing.T, env *testrig.Env, dir, source, body string) {
	t.Helper()
	res := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	data := baselineData(t, res.Stdout)
	if !data.Staged || data.Source != source || data.SHA256 != sha256Of(body) {
		t.Errorf("the staged Baseline did not survive: %+v", data)
	}
}

// materializedPaths lists the paths a setup envelope reported.
func materializedPaths(t *testing.T, stdout string) []string {
	t.Helper()
	var data struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	testrig.DataOf(t, stdout, &data)
	paths := make([]string, 0, len(data.Files))
	for _, file := range data.Files {
		paths = append(paths, file.Path)
	}
	return paths
}

// The next fresh Gateway launch restores from the staged Baseline: igdev hands
// Compose the restore argument, the rendered Compose file interpolates it into
// the Gateway's launcher command, and the staged file is mounted at the path
// that argument names. Clearing the Baseline takes the wiring back out.
func TestGatewayUpCarriesTheStagedBaseline(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, stamp := gatewayFixture(t, env, testrig.MinimalContract)
	source := backupSource(t, env, "backups/customer.gwbk", "gateway backup bytes\n", "<SOURCE>")
	testrig.ServeGateway(t, stamp.Ports.HTTP, map[string]int{"/": 200})

	up := env.RunIn(dir, "gateway", "up")
	testrig.WantExit(t, up, contract.ExitOK)
	if got := lastCall(t, env).GatewayEnv("GATEWAY_RESTORE_ARGS"); got != "" {
		t.Errorf("up without a staged Baseline handed the engine %q, want no restore argument", got)
	}

	stage(t, env, dir, source)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	call := lastCall(t, env)
	if got := call.GatewayEnv("GATEWAY_RESTORE_ARGS"); got != baseline.Args() {
		t.Errorf("up handed the engine GATEWAY_RESTORE_ARGS=%q, want %q", got, baseline.Args())
	}

	// The wiring is in the rendered Compose file the engine was given: the
	// launcher command interpolates the variable, and the staged file's
	// directory is mounted where the restore argument points.
	composeFile, _ := composePaths(dir)
	body, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatalf("read the Compose file: %v", err)
	}
	if !strings.Contains(string(body), "${GATEWAY_RESTORE_ARGS:-}") {
		t.Errorf("the Compose file does not interpolate the restore argument:\n%s", body)
	}
	mount := mountLine(t, string(body))
	wantMount := baselineDir(dir) + ":" + baseline.MountPoint + ":ro"
	if mount != wantMount {
		t.Errorf("Baseline mount = %q, want %q", mount, wantMount)
	}
	if !strings.HasPrefix(baseline.ContainerPath(), baseline.MountPoint+"/") {
		t.Errorf("the restore argument points outside the mount: %s", baseline.ContainerPath())
	}
	env.Golden(t, "baseline_up_wiring.txt", fmt.Sprintf(
		"argv:         %s\nrestore args: %s\nmount:        %s\n",
		strings.Join(call.Argv, " "), call.GatewayEnv("GATEWAY_RESTORE_ARGS"), mount))

	// reset is the fresh launch the restore applies to, so it carries the
	// wiring too.
	testrig.WantExit(t, env.RunIn(dir, "gateway", "reset", "--timeout", "5"), contract.ExitOK)
	if got := lastCall(t, env).GatewayEnv("GATEWAY_RESTORE_ARGS"); got != baseline.Args() {
		t.Errorf("reset handed the engine GATEWAY_RESTORE_ARGS=%q, want %q", got, baseline.Args())
	}

	// clear takes the wiring out: the next up restores from nothing.
	testrig.WantExit(t, env.RunIn(dir, "baseline", "clear"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "gateway", "up"), contract.ExitOK)
	if got := lastCall(t, env).GatewayEnv("GATEWAY_RESTORE_ARGS"); got != "" {
		t.Errorf("up after clear handed the engine %q, want no restore argument", got)
	}

	testrig.WantExit(t, env.RunIn(dir, "gateway", "down", "--volumes"), contract.ExitOK)
	env.AssertNoDockerOrphans(t)
}

// lastCall is the most recent invocation the engine recorded.
func lastCall(t *testing.T, env *testrig.Env) testrig.DockerCall {
	t.Helper()
	calls := env.DockerCalls(t)
	if len(calls) == 0 {
		t.Fatal("the engine was never called")
	}
	return calls[len(calls)-1]
}

// mountLine is the rendered Compose file's Baseline mount, normalized.
func mountLine(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") && strings.Contains(trimmed, ":"+baseline.MountPoint+":ro") {
			return strings.TrimPrefix(trimmed, "- ")
		}
	}
	t.Fatalf("the Compose file mounts no Baseline path:\n%s", body)
	return ""
}

// The staged Baseline's record is provenance, not a second source of truth: a
// record igdev cannot read never hides a staged file, and it is never a reason
// for status to fail.
func TestBaselineStatusToleratesABrokenRecord(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	source := backupSource(t, env, "backups/customer.gwbk", "gateway backup bytes\n", "<SOURCE>")
	stage(t, env, dir, source)

	if err := os.WriteFile(baseline.RecordPath(baselineDir(dir)), []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("corrupt the Baseline record: %v", err)
	}
	res := env.RunIn(dir, "baseline", "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	data := baselineData(t, res.Stdout)
	if !data.Staged {
		t.Errorf("an unreadable record hid a staged Baseline: %+v", data)
	}
	if data.Source != "" || data.SHA256 != "" {
		t.Errorf("status reported provenance it could not read: %+v", data)
	}
	env.AssertNoDockerCalls(t)
}

// The record igdev writes is what status reads, so it has to parse.
func TestBaselineRecordShape(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := baselineFixture(t, env)
	source := backupSource(t, env, "backups/customer.gwbk", "gateway backup bytes\n", "<SOURCE>")
	stage(t, env, dir, source)

	raw, err := os.ReadFile(baseline.RecordPath(baselineDir(dir)))
	if err != nil {
		t.Fatalf("read the Baseline record: %v", err)
	}
	var record struct {
		Source   string `json:"source"`
		Bytes    int64  `json:"bytes"`
		SHA256   string `json:"sha256"`
		StagedAt string `json:"staged_at"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("the Baseline record is not JSON: %v\n%s", err, raw)
	}
	if record.Source != source || record.SHA256 != sha256Of("gateway backup bytes\n") || record.StagedAt == "" {
		t.Errorf("record = %+v, want the staged source, digest, and timestamp", record)
	}
	env.AssertNoDockerCalls(t)
}
