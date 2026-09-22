package itest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// setup materializes the checkout. These tests assert the whole observable
// change set of a run: the files that appear, what the record holds, what the
// output carries, and that nothing else in the scratch machine moved.
//
// The Instance identity and the ports are random by design, so both are
// normalized out of goldens and asserted by reading the record the CLI wrote.

// setupStampOf reads the Checkout Setup record the CLI wrote.
func setupStampOf(t *testing.T, root string) gate.Stamp {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, project.StateDir, project.SetupRecord))
	if err != nil {
		t.Fatalf("read the Checkout Setup record: %v", err)
	}
	stamp, ok := gate.Decode(raw)
	if !ok {
		t.Fatalf("setup.json is not a Setup Stamp:\n%s", raw)
	}
	return stamp
}

// localCredentials reads the checkout-local tier the CLI wrote.
func localCredentials(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, project.StateDir, project.LocalConfig))
	if err != nil {
		t.Fatalf("read the checkout-local config: %v", err)
	}
	return string(raw)
}

// passwordOf extracts the password the CLI recorded.
func passwordOf(t *testing.T, root string) string {
	t.Helper()
	body := localCredentials(t, root)
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "admin_password = "); ok {
			password, err := strconv.Unquote(value)
			if err != nil {
				t.Fatalf("admin_password is not a quoted string: %v\n%s", err, body)
			}
			return password
		}
	}
	t.Fatalf("local config carries no admin_password:\n%s", body)
	return ""
}

// normalizeInstance makes a run's random identity and ports collapse to tokens,
// so a golden can be a literal.
func normalizeInstance(t *testing.T, env *testrig.Env, stamp gate.Stamp) {
	t.Helper()
	env.RegisterReplacement(stamp.InstanceID, "<INSTANCE_ID>")
	env.RegisterReplacement(stamp.InstanceID[:8], "<SHORT_ID>")
	for token, port := range map[string]int{
		"<PORT_HTTP>":  stamp.Ports.HTTP,
		"<PORT_HTTPS>": stamp.Ports.HTTPS,
		"<PORT_DEBUG>": stamp.Ports.Debug,
	} {
		env.RegisterReplacement(strconv.Itoa(port), token)
	}
}

// normalizeConsent makes the machine Consent timestamps collapse to tokens.
func normalizeConsent(t *testing.T, env *testrig.Env) {
	t.Helper()
	record, err := consent.Load(consent.Path(filepath.Join(env.Home, ".config", "igdev")))
	if err != nil {
		t.Fatalf("read the Consent record: %v", err)
	}
	for _, term := range consent.Terms() {
		if accepted, ok := record.Accepted(term); ok {
			env.RegisterReplacement(accepted.AcceptedAt, "<ACCEPTED_AT:"+term.ID+">")
		}
	}
}

// holdPorts binds a triplet in the test process, standing in for a running
// Gateway, so a later allocation must avoid those ports.
func holdPorts(t *testing.T, triplet ports.Triplet) {
	t.Helper()
	for _, port := range triplet.All() {
		listener, err := net.Listen("tcp", net.JoinHostPort(ports.BindAddress, strconv.Itoa(port)))
		if err != nil {
			t.Fatalf("hold port %d: %v", port, err)
		}
		t.Cleanup(func() { listener.Close() })
	}
}

// repoEntries lists a fixture repository's top-level entries, sorted.
func repoEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// Consent comes first: without the machine-global record nothing is materialized
// and the failure is the frozen human-required shape.
func TestSetupRefusesWithoutConsent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	base := env.Snapshot()

	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitHumanAction)
	env.Golden(t, "setup_consent_required.json", res.Stdout)

	envelope := testrig.Envelope(t, res.Stdout)
	testrig.WantCode(t, envelope, contract.CodeConsentRequired)
	testrig.WantRemediation(t, envelope, "igdev setup --accept-eula")
	if !strings.Contains(envelope.Message, "Ignition EULA") {
		t.Errorf("message does not name the term: %q", envelope.Message)
	}
	// A refused run writes nothing at all: no Consent record, no .igdev/.
	res.AssertNoLeaks(t)

	// Human mode reports the same thing as prose on stderr, and stdout stays
	// empty so a pipe never receives it.
	human := env.RunIn(dir, "setup")
	testrig.WantExit(t, human, contract.ExitHumanAction)
	env.Golden(t, "setup_consent_required.txt", human.Stderr)
	if human.Stdout != "" {
		t.Errorf("human failure wrote to stdout: %q", human.Stdout)
	}
	human.AssertNoLeaks(t)
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a refused setup changed %v", changes)
	}
}

// A human accepts once per machine; everything after that runs unattended, in
// this checkout and in a brand-new one.
func TestSetupAcceptsConsentOncePerMachine(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	accepted := env.RunIn(dir, "setup", "--accept-eula")
	testrig.WantExit(t, accepted, contract.ExitOK)
	if !strings.Contains(accepted.Stdout, "ignition-eula") {
		t.Errorf("the accepting run does not report the term it recorded:\n%s", accepted.Stdout)
	}

	recordPath := consent.Path(filepath.Join(env.Home, ".config", "igdev"))
	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatalf("no Consent record at %s: %v", recordPath, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("Consent record mode = %o, want 600", info.Mode().Perm())
	}
	record, err := consent.Load(recordPath)
	if err != nil {
		t.Fatalf("load Consent record: %v", err)
	}
	if _, ok := record.Accepted(consent.EULA); !ok {
		t.Fatalf("the EULA acceptance was not recorded:\n%s", record.Encode())
	}
	if _, ok := record.Accepted(consent.ModuleLicense); ok {
		t.Error("setup recorded a term it was not asked to accept")
	}

	// Unattended from here on: no flags, no prompts, exit 0.
	unattended := env.RunIn(dir, "setup")
	testrig.WantExit(t, unattended, contract.ExitOK)
	if !strings.Contains(unattended.Stdout, "already accepted") {
		t.Errorf("the unattended run did not report the existing Consent:\n%s", unattended.Stdout)
	}

	// A second checkout of the same machine needs no second ritual.
	worktree := env.Mkdir("worktree")
	testrig.WantExit(t, env.RunIn(worktree, "init"), contract.ExitOK)
	fresh := env.RunIn(worktree, "setup", "--json")
	testrig.WantExit(t, fresh, contract.ExitOK)

	// The human command is idempotent: an already-accepted machine is a no-op
	// consent write, which the record's own bytes show.
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read Consent record: %v", err)
	}
	again := env.RunIn(dir, "setup", "--accept-eula", "--json")
	testrig.WantExit(t, again, contract.ExitOK)
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read Consent record: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("re-accepting rewrote the Consent record:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	var data struct {
		Consent []string `json:"consent_accepted"`
	}
	testrig.DataOf(t, again.Stdout, &data)
	if len(data.Consent) != 0 {
		t.Errorf("consent_accepted = %v on an already-accepted machine, want none", data.Consent)
	}
}

// The first setup materializes exactly the Checkout Setup and nothing else, and
// the record it writes is the whole machine-facing identity of the Instance.
func TestSetupMaterializesCheckoutState(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	base := env.Snapshot()

	// Re-running from the same state must produce the same machine-readable
	// answer, so the golden is taken from a run with the identity normalized.
	stamp := setupStampOf(t, dir)
	normalizeInstance(t, env, stamp)
	normalizeConsent(t, env)
	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "setup_current.json", res.Stdout)

	// The record is the Instance identity, the Contract Digest, and the port
	// triplet.
	contractRaw, err := os.ReadFile(filepath.Join(dir, project.ContractFile))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	if stamp.ContractDigest != project.Digest(contractRaw) {
		t.Errorf("record digest = %s, want the Contract Digest", stamp.ContractDigest)
	}
	if stamp.ContractSchema != project.LatestSchema || stamp.Schema != gate.StampSchema {
		t.Errorf("record schemas = contract %d / record %d", stamp.ContractSchema, stamp.Schema)
	}
	if stamp.CLIContract != contract.Version {
		t.Errorf("record CLI contract = %s, want %s", stamp.CLIContract, contract.Version)
	}
	if !strings.Contains(stamp.InstanceID, "-") || len(stamp.InstanceID) != 36 {
		t.Errorf("instance_id = %q, want a UUID", stamp.InstanceID)
	}
	if !stamp.Ports.Complete() {
		t.Errorf("record ports = %+v, want the three allocated ports", stamp.Ports)
	}
	if stamp.CreatedAt == "" {
		t.Error("record carries no created_at")
	}

	// The label is derived from the UUID, not from the path: two checkouts of
	// the same bytes share nothing here.
	label := "igdev-" + strings.ReplaceAll(stamp.InstanceID, "-", "")[:8]
	envFile, err := os.ReadFile(filepath.Join(dir, ".igdev", "runtime", "compose.env"))
	if err != nil {
		t.Fatalf("read compose.env: %v", err)
	}
	for _, want := range []string{
		"COMPOSE_PROJECT_NAME=" + label,
		"GATEWAY_NAME=" + label,
		fmt.Sprintf("GATEWAY_HTTP_PORT=%d", stamp.Ports.HTTP),
		fmt.Sprintf("GATEWAY_HTTPS_PORT=%d", stamp.Ports.HTTPS),
		fmt.Sprintf("GATEWAY_DEBUG_PORT=%d", stamp.Ports.Debug),
		"IGDEV_INSTANCE_ID=" + stamp.InstanceID,
	} {
		if !strings.Contains(string(envFile), want) {
			t.Errorf("compose.env does not carry %q:\n%s", want, envFile)
		}
	}

	// Ports are a property of the machine: the tracked contract never holds one
	// (ADR 0003), and setup must not have rewritten it.
	for _, port := range stamp.Ports.All() {
		if strings.Contains(string(contractRaw), strconv.Itoa(port)) {
			t.Errorf("the Project Contract carries the allocated port %d:\n%s", port, contractRaw)
		}
	}
	// The repository holds no tool infrastructure: everything igdev generated
	// lives in the gitignored .igdev/.
	if got := strings.Join(repoEntries(t, dir), ","); got != ".gitignore,.igdev,igdev.toml" {
		t.Errorf("repository holds %s, want only the contract, .gitignore, and .igdev/", got)
	}
	// The password is private, and so is the record.
	for _, name := range []string{"local.toml", "setup.json"} {
		info, err := os.Stat(filepath.Join(dir, ".igdev", name))
		if err != nil {
			t.Fatalf("stat .igdev/%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf(".igdev/%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
	env.AssertNoLeaksOutside(t, base, dir, env.Home)
	env.AssertNoPartialWrites(t)
}

// Human mode reports the same materialization as prose, and holds the same rule
// about the password.
func TestSetupHumanOutput(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	res := env.RunIn(dir, "setup", "--accept-eula")
	testrig.WantExit(t, res, contract.ExitOK)
	normalizeInstance(t, env, setupStampOf(t, dir))
	env.Golden(t, "setup_human.txt", res.Stdout)

	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}
	password := passwordOf(t, dir)
	if strings.Contains(res.Stdout, password) || strings.Contains(res.Stderr, password) {
		t.Errorf("human output carries the password:\n%s%s", res.Stdout, res.Stderr)
	}
	for _, want := range []string{"instance:", "ports:", "credentials:", "materialized:"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("human output does not report %q:\n%s", want, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "admin_password") {
		t.Errorf("human output names the credential key:\n%s", res.Stdout)
	}
}

// The exact change set of the first setup: the Checkout Setup inside the
// fixture, plus the machine Consent record under HOME, and nothing anywhere else.
func TestSetupWritesExactlyTheCheckoutSetup(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	base := env.Snapshot()

	res := env.RunIn(dir, "setup", "--accept-eula")
	testrig.WantExit(t, res, contract.ExitOK)

	want := []string{
		"home/.config",
		"home/.config/igdev",
		"home/.config/igdev/accepted.toml",
		"repo/.igdev",
		"repo/.igdev/local.toml",
		"repo/.igdev/modules",
		"repo/.igdev/restore",
		"repo/.igdev/runtime",
		"repo/.igdev/runtime/Dockerfile",
		"repo/.igdev/runtime/compose.env",
		"repo/.igdev/runtime/compose.yaml",
		"repo/.igdev/setup.json",
	}
	if got := env.Added(base); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("setup added %v\nwant %v\nfull tree:\n%s", got, want, env.Tree())
	}
	// setup mutates nothing that git tracks: everything it does is an addition
	// inside the disposable state, never a rewrite of the contract or .gitignore.
	for _, change := range env.Changes(base) {
		if change.Kind != "added" {
			t.Errorf("setup %s %s, which it must not touch", change.Kind, change.Path)
		}
	}
	env.AssertNoLeaksOutside(t, base, dir, env.Home)
}

// A second setup on a current checkout re-materializes nothing: the same
// instance_id, the same ports, and bytes that did not move.
func TestSetupIsIdempotent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)

	first := setupStampOf(t, dir)
	base := env.Snapshot()

	stamp := setupStampOf(t, dir)
	normalizeInstance(t, env, stamp)
	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "setup_again.json", res.Stdout)

	var data struct {
		Instance string `json:"instance_id"`
		Files    []struct {
			Path   string `json:"path"`
			Action string `json:"action"`
		} `json:"files"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Instance != first.InstanceID {
		t.Errorf("instance_id = %s, want the recorded %s", data.Instance, first.InstanceID)
	}
	if len(data.Files) == 0 {
		t.Fatal("the run reported no materialized paths")
	}
	for _, file := range data.Files {
		if file.Action != "unchanged" {
			t.Errorf("%s reported %q on a second identical run, want unchanged", file.Path, file.Action)
		}
	}
	env.AssertNoLeaksOutside(t, base, dir, env.Home)
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a second identical setup changed %v", changes)
	}
}

// A contract that moved leaves the checkout stale. setup refreshes the record to
// the current contract while keeping the Instance: same identity, same ports,
// and the rendered runtime follows the contract.
func TestSetupRefreshesStaleContractKeepingTheInstance(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	before := setupStampOf(t, dir)

	testrig.WantExit(t, env.RunIn(dir, "init", "--gateway-memory-mb", "4096"), contract.ExitOK)
	if state := testrig.Status(t, env.RunIn(dir, "status", "--json").Stdout); state.Setup.StampState != "stale" {
		t.Fatalf("stamp_state = %q after the contract moved, want stale", state.Setup.StampState)
	}

	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	after := setupStampOf(t, dir)
	if after.InstanceID != before.InstanceID {
		t.Errorf("instance_id moved from %s to %s", before.InstanceID, after.InstanceID)
	}
	if after.Ports != before.Ports {
		t.Errorf("ports moved from %+v to %+v although they were free", before.Ports, after.Ports)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Errorf("created_at moved from %s to %s", before.CreatedAt, after.CreatedAt)
	}
	if after.ContractDigest == before.ContractDigest {
		t.Error("the record still carries the old Contract Digest after a refresh")
	}
	contractRaw, err := os.ReadFile(filepath.Join(dir, project.ContractFile))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	if after.ContractDigest != project.Digest(contractRaw) {
		t.Errorf("refreshed digest = %s, want the current Contract Digest", after.ContractDigest)
	}
	if err := gate.Require(gateInputOf(t, env, dir)); err != nil {
		t.Errorf("the refreshed checkout is still refused: %v", err)
	}
	compose, err := os.ReadFile(filepath.Join(dir, ".igdev", "runtime", "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(compose), "-m 4096") {
		t.Errorf("the rendered runtime did not follow the contract:\n%s", compose)
	}
}

// A recorded port another process holds is re-allocated: the record must never
// describe a collision, and the Instance keeps its identity.
func TestSetupReallocatesPortsThatWereTaken(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	before := setupStampOf(t, dir)

	// Something else is now listening on the recorded ports, as a Gateway would be.
	holdPorts(t, before.Ports)

	testrig.WantExit(t, env.RunIn(dir, "setup", "--json"), contract.ExitOK)
	after := setupStampOf(t, dir)
	if after.InstanceID != before.InstanceID {
		t.Errorf("instance_id moved from %s to %s", before.InstanceID, after.InstanceID)
	}
	if after.Ports == before.Ports {
		t.Fatalf("ports were reused although every one of them was taken: %+v", after.Ports)
	}
	for _, port := range after.Ports.All() {
		for _, taken := range before.Ports.All() {
			if port == taken {
				t.Errorf("re-allocated triplet %+v reuses the taken port %d", after.Ports, port)
			}
		}
	}
	if !after.Ports.Complete() {
		t.Errorf("re-allocated ports = %+v, want a complete triplet", after.Ports)
	}
}

// Two worktrees of one repository are two Instances: the same contract bytes,
// two identities, two namespaces, and disjoint ports even while the first
// worktree's Gateway holds its own.
func TestSetupGivesWorktreesDisjointInstances(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	one := env.Mkdir("one")
	testrig.WantExit(t, env.RunIn(one, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(one, "setup", "--accept-eula"), contract.ExitOK)
	first := setupStampOf(t, one)

	// The second worktree carries the same contract bytes: the identity may not
	// depend on the contract or on the path.
	two := env.Mkdir("two")
	raw, err := os.ReadFile(filepath.Join(one, project.ContractFile))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(two, project.ContractFile), raw, 0o644); err != nil {
		t.Fatalf("write worktree contract: %v", err)
	}
	holdPorts(t, first.Ports)

	res := env.RunIn(two, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	second := setupStampOf(t, two)

	if second.ContractDigest != first.ContractDigest {
		t.Fatalf("the worktrees do not share a contract: %s vs %s", second.ContractDigest, first.ContractDigest)
	}
	if second.InstanceID == first.InstanceID {
		t.Errorf("both worktrees report Instance %s", second.InstanceID)
	}
	firstLabel := "igdev-" + strings.ReplaceAll(first.InstanceID, "-", "")[:8]
	secondLabel := "igdev-" + strings.ReplaceAll(second.InstanceID, "-", "")[:8]
	if firstLabel == secondLabel {
		t.Errorf("both worktrees share the Docker namespace %s", firstLabel)
	}
	for _, port := range second.Ports.All() {
		if port == first.Ports.HTTP || port == first.Ports.HTTPS || port == first.Ports.Debug {
			t.Errorf("the second worktree kept %d, which the first holds: %+v", port, second.Ports)
		}
	}
	// The label the record implies is the label the runtime files carry.
	envFile, err := os.ReadFile(filepath.Join(two, ".igdev", "runtime", "compose.env"))
	if err != nil {
		t.Fatalf("read compose.env: %v", err)
	}
	if !strings.Contains(string(envFile), "COMPOSE_PROJECT_NAME="+secondLabel) {
		t.Errorf("compose.env does not carry the Instance namespace %s:\n%s", secondLabel, envFile)
	}
}

// The admin password is generated at setup, stored 0600, and never printed — in
// either dialect, from the generated path or from an override.
func TestSetupNeverPrintsTheAdminPassword(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	for _, tc := range []struct {
		name string
		flag string
		env  []string
	}{
		{name: "generated"},
		{name: "environment override", env: []string{"IGDEV_GATEWAY_ADMIN_PASSWORD=ci-password-ci-password"}},
		{name: "flag override", flag: "flag-password-flag-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"setup", "--accept-eula", "--json"}
			if tc.flag != "" {
				args = append(args, "--admin-password", tc.flag)
			}
			res := env.Run(testrig.Run{Args: args, Dir: dir, Env: tc.env})
			testrig.WantExit(t, res, contract.ExitOK)

			password := passwordOf(t, dir)
			if len(password) < 16 {
				t.Fatalf("recorded password is too short to be real: %q", password)
			}
			for _, observed := range []string{res.Stdout, res.Stderr} {
				if strings.Contains(observed, password) {
					t.Errorf("the password appears in the run's own output:\n%s", observed)
				}
			}
			if strings.Contains(res.Stdout, "admin_password") {
				t.Errorf("the envelope carries the credential key:\n%s", res.Stdout)
			}

			// Human mode is held to the same rule.
			human := env.MustRun("setup")
			for _, observed := range []string{human.Stdout, human.Stderr} {
				if strings.Contains(observed, password) {
					t.Errorf("the password appears in human output:\n%s", observed)
				}
			}
		})
	}
}

// An override reaches the file, and the default username is recorded.
func TestSetupCredentialsOverrides(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)

	res := env.Run(testrig.Run{
		Args: []string{"setup", "--accept-eula", "--json"},
		Dir:  dir,
		Env: []string{
			"IGDEV_GATEWAY_ADMIN_PASSWORD=from-the-environment-value",
			"IGDEV_GATEWAY_ADMIN_USERNAME=operator",
		},
	})
	testrig.WantExit(t, res, contract.ExitOK)

	var data struct {
		Credentials struct {
			Source   string `json:"source"`
			Username string `json:"username"`
		} `json:"credentials"`
	}
	testrig.DataOf(t, res.Stdout, &data)
	if data.Credentials.Source != "environment" {
		t.Errorf("credential source = %q, want environment", data.Credentials.Source)
	}
	if data.Credentials.Username != "operator" {
		t.Errorf("username = %q, want operator", data.Credentials.Username)
	}
	body := localCredentials(t, dir)
	if !strings.Contains(body, `admin_password = "from-the-environment-value"`) {
		t.Errorf("the override did not reach the local config:\n%s", body)
	}
	if !strings.Contains(body, `admin_username = "operator"`) {
		t.Errorf("the username override did not reach the local config:\n%s", body)
	}
	if strings.Contains(res.Stdout, "from-the-environment-value") {
		t.Errorf("the override was echoed:\n%s", res.Stdout)
	}

	// A later run without the override keeps what the checkout already holds, so
	// a credential the Gateway is using is never rotated behind the operator.
	plain := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, plain, contract.ExitOK)
	testrig.DataOf(t, plain.Stdout, &data)
	if data.Credentials.Source != "existing" {
		t.Errorf("credential source = %q on a re-run, want existing", data.Credentials.Source)
	}
	if body := localCredentials(t, dir); !strings.Contains(body, `admin_password = "from-the-environment-value"`) {
		t.Errorf("a later run rotated the password:\n%s", body)
	}
}

// The runtime files come from the templates embedded in the binary, and the fake
// docker accepts them: the repository contributes no Docker input of its own.
func TestSetupRendersRuntimeFilesTheDockerShimAccepts(t *testing.T) {
	env := testrig.NewEnv(t)
	state := env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)
	stamp := setupStampOf(t, dir)

	compose, err := os.ReadFile(filepath.Join(dir, ".igdev", "runtime", "compose.yaml"))
	if err != nil {
		t.Fatalf("read the rendered Compose file: %v", err)
	}
	body := string(compose)
	for _, want := range []string{
		labelOf(t, stamp),
		fmt.Sprintf(`"%s:%d:8088"`, ports.BindAddress, stamp.Ports.HTTP),
		filepath.Join(dir, ".igdev", "modules") + ":/usr/local/bin/ignition/user-lib/modules",
		filepath.Join(dir, ".igdev", "restore") + ":/restore:ro",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Compose file does not carry %q:\n%s", want, body)
		}
	}

	// The shim renders it: the file igdev wrote is the file docker is handed, and
	// docker accepts it.
	res := env.RunShellIn(dir, "docker compose --env-file .igdev/runtime/compose.env -f .igdev/runtime/compose.yaml config")
	if res.Exit != 0 {
		t.Fatalf("docker compose config exited %d: %s", res.Exit, res.Stderr)
	}
	calls := env.DockerCalls(t)
	if len(calls) == 0 {
		t.Fatalf("docker was never invoked; state file %s", state)
	}
	last := strings.Join(calls[len(calls)-1].Argv, " ")
	if !strings.Contains(last, ".igdev/runtime/compose.yaml") || !strings.Contains(last, "config") {
		t.Errorf("docker was not asked to render the materialized Compose file: %q", last)
	}
	env.AssertNoDockerOrphans(t)

	// No tool infrastructure in the repository: nothing to .dockerignore, no
	// Dockerfile, no compose file of its own.
	if got := strings.Join(repoEntries(t, dir), ","); got != ".gitignore,.igdev,igdev.toml" {
		t.Errorf("repository holds %s, want only the contract, .gitignore, and .igdev/", got)
	}
}

// Outside a Project Root there is nothing to materialize, and the repair is init.
func TestSetupOutsideProjectFails(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("plain")
	base := env.Snapshot()

	res := env.RunIn(dir, "setup", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeNotInitialized)
	testrig.WantRemediation(t, testrig.Envelope(t, res.Stdout), "igdev init")
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a refused setup changed %v", changes)
	}
}

// setup is the repair path for the Checkout Setup, not for the contract: a schema
// this binary does not speak still fails closed, and nothing is written.
func TestSetupRefusesUnsupportedContractSchema(t *testing.T) {
	env := testrig.NewEnv(t)
	dir := env.Project("repo", "schema = 2\n\n[ignition]\nversion = \"9.0.0\"\n\n[future]\nnothing = true\n")
	base := env.Snapshot()

	res := env.RunIn(dir, "setup", "--accept-eula", "--json")
	testrig.WantExit(t, res, contract.ExitFailure)
	testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeContractSchemaUnsupported)
	if changes := env.Changes(base); len(changes) != 0 {
		t.Errorf("a refused setup changed %v", changes)
	}
}

// setup owns the checkout-local tier, so a tier igdev cannot parse is repaired
// rather than left to wedge every command that resolves config.
func TestSetupRepairsABrokenLocalTier(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	env.Write("repo/.igdev/local.toml", "this is not [ toml")

	res := env.RunIn(dir, "setup", "--accept-eula", "--json")
	testrig.WantExit(t, res, contract.ExitOK)

	if body := localCredentials(t, dir); !strings.Contains(body, "admin_password") {
		t.Errorf("the broken tier was not repaired:\n%s", body)
	}
	// Every command that resolves config works again.
	testrig.WantExit(t, env.RunIn(dir, "status", "--json"), contract.ExitOK)
}

// status surfaces the Instance identity, the ports, and the Consent state, so no
// caller has to read setup.json by hand or assume a port.
func TestStatusReportsInstancePortsAndConsent(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	dir := env.Mkdir("repo")
	testrig.WantExit(t, env.RunIn(dir, "init"), contract.ExitOK)
	testrig.WantExit(t, env.RunIn(dir, "setup", "--accept-eula"), contract.ExitOK)

	stamp := setupStampOf(t, dir)
	normalizeInstance(t, env, stamp)
	normalizeConsent(t, env)

	res := env.RunIn(dir, "status", "--json")
	testrig.WantExit(t, res, contract.ExitOK)
	env.Golden(t, "status_after_setup.json", res.Stdout)

	data := testrig.Status(t, res.Stdout)
	if data.Setup.StampState != "current" {
		t.Errorf("stamp_state = %q, want current", data.Setup.StampState)
	}
	if data.Setup.InstanceID != stamp.InstanceID {
		t.Errorf("instance_id = %q, want the recorded %q", data.Setup.InstanceID, stamp.InstanceID)
	}
	if data.Setup.Namespace != labelOf(t, stamp) {
		t.Errorf("namespace = %q, want %q", data.Setup.Namespace, labelOf(t, stamp))
	}
	if data.Setup.Ports == nil {
		t.Fatal("status reports no ports although the record carries them")
	}
	if data.Setup.Ports.HTTP != stamp.Ports.HTTP || data.Setup.Ports.HTTPS != stamp.Ports.HTTPS ||
		data.Setup.Ports.Debug != stamp.Ports.Debug {
		t.Errorf("status ports %+v do not match the record %+v", data.Setup.Ports, stamp.Ports)
	}

	byID := map[string]bool{}
	for _, term := range data.Consent.Terms {
		byID[term.ID] = term.Accepted
		if term.Accepted && (term.AcceptedAt == "" || term.CLIVersion == "") {
			t.Errorf("term %s is accepted with no evidence: %+v", term.ID, term)
		}
	}
	if len(data.Consent.Terms) != len(consent.Terms()) {
		t.Errorf("status reports %d terms, want %d", len(data.Consent.Terms), len(consent.Terms()))
	}
	if !byID[consent.EULA.ID] {
		t.Error("the accepted EULA is not reported as accepted")
	}
	if byID[consent.ModuleLicense.ID] {
		t.Error("a term that was never accepted is reported as accepted")
	}

	// Before setup the same fields say so instead of inventing values.
	fresh := env.Mkdir("fresh")
	testrig.WantExit(t, env.RunIn(fresh, "init"), contract.ExitOK)
	unset := testrig.Status(t, env.RunIn(fresh, "status", "--json").Stdout)
	if unset.Setup.StampState != "required" || unset.Setup.InstanceID != "" || unset.Setup.Ports != nil {
		t.Errorf("status before setup = %+v, want required and empty", unset.Setup)
	}
}

// labelOf is the Docker namespace the record implies.
func labelOf(t *testing.T, stamp gate.Stamp) string {
	t.Helper()
	return "igdev-" + strings.ReplaceAll(stamp.InstanceID, "-", "")[:8]
}
