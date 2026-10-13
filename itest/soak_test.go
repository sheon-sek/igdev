package itest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The opt-in real-Gateway soak (igdev#151). It drives one disposable Instance of
// its own through the lifecycle the resource fixes of igdev#140 changed, on the
// real Docker engine, and records trends: repeated up/down/cleanup with a fixed
// seed, rebuilds with a changing seed, an optional [ignition].version change
// reclaimed by `cleanup --obsolete-images`, and a long-running Gateway sampled at an
// interval. docs/lifecycle-matrix.md is its manual: the workload, the environment
// variables, what it asserts and what it only observes.
//
// It never runs by default: `go test ./...`, `make all` and CI skip it unless
// IGDEV_SOAK=1. Every mutation is an igdev verb run in the soak's own checkout;
// the soak's own docker calls are read-only (soakDocker refuses anything else).
// Consent is a person's: the soak reads the machine record through
// IGDEV_CONSENT_FILE, under which igdev refuses --accept-eula, so it cannot accept
// the EULA on anyone's behalf.
const (
	soakEnable      = "IGDEV_SOAK"
	soakComposeKey  = "com.docker.compose.project"
	soakContainerFS = "/usr/local/bin/ignition/data"
)

type soakConfig struct {
	cycles, rebuilds     int
	duration, interval   time.Duration
	waitTimeout          time.Duration
	version, oldVersion  string
	reportDir, consent   string
	memoryMB             int
	teardownAllowance    time.Duration
	dockerHost, dockerAt string
}

func soakConfigFromEnv(t *testing.T) soakConfig {
	t.Helper()
	cfg := soakConfig{
		cycles:            soakEnvInt(t, "IGDEV_SOAK_CYCLES", 2),
		rebuilds:          soakEnvInt(t, "IGDEV_SOAK_REBUILDS", 2),
		duration:          soakEnvDuration(t, "IGDEV_SOAK_DURATION", 10*time.Minute),
		interval:          soakEnvDuration(t, "IGDEV_SOAK_INTERVAL", time.Minute),
		waitTimeout:       soakEnvDuration(t, "IGDEV_SOAK_WAIT_TIMEOUT", 10*time.Minute),
		version:           os.Getenv("IGDEV_SOAK_VERSION"),
		oldVersion:        os.Getenv("IGDEV_SOAK_OLD_VERSION"),
		reportDir:         os.Getenv("IGDEV_SOAK_REPORT_DIR"),
		consent:           os.Getenv(consent.FileEnv),
		memoryMB:          soakEnvInt(t, "IGDEV_SOAK_MEMORY_MB", 2048),
		teardownAllowance: 15 * time.Minute,
	}
	if cfg.consent == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			t.Fatalf("locate the machine Consent record: %v", err)
		}
		cfg.consent = filepath.Join(dir, "igdev", "accepted.toml")
	}
	return cfg
}

func soakEnvInt(t *testing.T, name string, def int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("%s=%q: want a non-negative integer", name, raw)
	}
	return n
}

func soakEnvDuration(t *testing.T, name string, def time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		t.Fatalf("%s=%q: want a duration such as 10m", name, raw)
	}
	return d
}

// soak is one run: its scratch environment, its checkout, and what it recorded.
type soak struct {
	t   *testing.T
	cfg soakConfig
	env *testrig.Env
	// dir is the soak's checkout. It lives in the report directory, so a failed
	// teardown leaves it, and its Checkout Setup, for `igdev cleanup` to finish.
	dir string
	// ns is the Instance the checkout's Setup names now. A cleanup removes the
	// Setup, and the next setup mints a new Instance, so namespaces is every one
	// the soak created: the teardown checks all of them.
	ns         string
	namespaces []string

	// before is every engine object present when the soak started.
	before soakEngine

	mu       sync.Mutex
	samples  []soakSample
	commands *os.File
	evidence []string
	notes    []string
	signal   chan os.Signal
	stopped  os.Signal
	started  time.Time
	kept     int
}

func TestSoakRealGateway(t *testing.T) {
	if os.Getenv(soakEnable) != "1" {
		t.Skipf("the real-Gateway soak is opt-in: set %s=1 (docs/lifecycle-matrix.md)", soakEnable)
	}
	s := newSoak(t, soakConfigFromEnv(t))
	s.run()
}

func newSoak(t *testing.T, cfg soakConfig) *soak {
	t.Helper()
	if _, err := os.Stat(cfg.consent); err != nil {
		t.Fatalf("no Consent record at %s: consent is human-only, so a person runs `igdev setup --accept-eula` "+
			"(or exports one with `igdev consent export`) before the soak; it never accepts the EULA itself", cfg.consent)
	}
	reportDir, checkout, err := soakClaimDirs(cfg.reportDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.reportDir = reportDir
	s := &soak{t: t, cfg: cfg, dir: checkout, started: time.Now()}

	// The engine igdev talks to must be the one the soak inspects: igdev runs with a
	// scratch HOME, so the invoking user's current Docker context is pinned here.
	s.cfg.dockerHost = os.Getenv("DOCKER_HOST")
	if s.cfg.dockerHost == "" {
		host, err := s.docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			t.Fatalf("the soak needs a reachable Docker engine: %v", err)
		}
		s.cfg.dockerHost = strings.TrimSpace(host)
	}
	if _, err := s.docker("info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("the soak needs a reachable Docker engine at %s: %v", s.cfg.dockerHost, err)
	}
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		t.Fatalf("docker is not on PATH: %v", err)
	}
	s.cfg.dockerAt = filepath.Dir(dockerBin)

	s.env = testrig.NewEnv(t)
	path := ""
	for _, pair := range s.env.EnvOverlay() {
		if value, ok := strings.CutPrefix(pair, "PATH="); ok {
			path = value
		}
	}
	s.env.SetBaseEnv("PATH=" + s.cfg.dockerAt + string(os.PathListSeparator) + path)
	s.env.SetBaseEnv("DOCKER_HOST=" + s.cfg.dockerHost)
	s.env.SetBaseEnv(consent.FileEnv + "=" + cfg.consent)

	commands, err := os.OpenFile(filepath.Join(cfg.reportDir, "commands.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("create the command log: %v", err)
	}
	s.commands = commands
	s.before = s.engine()

	// An interrupted runner stops between steps and still runs the teardown, which
	// removes only this checkout's Instance. Ctrl-C reaches igdev too (one process
	// group), and igdev cleans up its own temporary state (igdev#149).
	s.signal = make(chan os.Signal, 2)
	signal.Notify(s.signal, os.Interrupt, syscall.SIGTERM)
	t.Cleanup(s.teardown)
	t.Logf("soak report: %s", cfg.reportDir)
	return s
}

// soakClaimDirs creates the soak's report directory and the checkout inside it,
// both exclusively. The soak must own every Instance it touches, and a checkout
// that already exists may hold a Setup whose Instance (and its data volume)
// belongs to someone else: init keeps its contract, setup keeps its Instance, and
// the soak's cleanups would then remove that Instance. So an existing report
// directory, a symlink in its place, or an existing checkout is refused before
// anything is written. With no report directory named, a fresh one is allocated
// under $TMPDIR, unique even for two runs started in the same second.
func soakClaimDirs(reportDir string) (string, string, error) {
	if reportDir == "" {
		dir, err := os.MkdirTemp("", "igdev-soak-"+time.Now().UTC().Format("20060102T150405Z")+"-")
		if err != nil {
			return "", "", fmt.Errorf("create the report directory: %w", err)
		}
		reportDir = dir
	} else {
		if err := os.MkdirAll(filepath.Dir(reportDir), 0o755); err != nil {
			return "", "", fmt.Errorf("create the report directory's parent: %w", err)
		}
		if err := os.Mkdir(reportDir, 0o755); err != nil {
			return "", "", fmt.Errorf("IGDEV_SOAK_REPORT_DIR=%s: the soak needs a directory it creates itself, so it never adopts "+
				"an existing checkout or its Instance; name a path that does not exist yet: %w", reportDir, err)
		}
	}
	checkout := filepath.Join(reportDir, "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		return "", "", fmt.Errorf("create the soak checkout: %w", err)
	}
	return reportDir, checkout, nil
}

// soakCheckRoot requires that igdev, run in checkout, discovers want as its
// Project Root. Discovery walks upward to the nearest igdev.toml, and init writes
// to the root it finds, so a fresh checkout under another project (a relative
// IGDEV_SOAK_REPORT_DIR resolves under itest/, inside this repository) would have
// init rewrite that project's contract. Before init want is "": no project above
// the checkout. After init it is the checkout itself. The binary answers, through
// `igdev status --json`, so the check walks exactly the path init walks.
func soakCheckRoot(env *testrig.Env, checkout, want string) error {
	res := env.Run(testrig.Run{Args: []string{"status", "--json"}, Dir: checkout})
	if res.Err != nil {
		return fmt.Errorf("igdev status in %s: %w", checkout, res.Err)
	}
	var status struct {
		ProjectRoot string `json:"project_root"`
	}
	if err := soakData(res.Stdout, &status); err != nil {
		return fmt.Errorf("igdev status in %s printed no readable envelope (exit %d): %w", checkout, res.Exit, err)
	}
	got := status.ProjectRoot
	switch {
	case want == "" && got != "":
		return fmt.Errorf("the soak checkout %s lies inside the igdev project at %s, so init would rewrite that project's contract; "+
			"set IGDEV_SOAK_REPORT_DIR to an absolute path outside every igdev project", checkout, got)
	case want != "" && !soakSamePath(got, want):
		return fmt.Errorf("after init, igdev in %s finds the Project Root %q, want the soak checkout %s", checkout, got, want)
	}
	return nil
}

// soakSamePath reports whether a and b name the same directory once symlinks
// are resolved.
func soakSamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// soakPreexisting lists what the engine held of namespace ns before the soak
// started. A namespace the soak's own Setup names must be one it minted, so any
// object of it already on the engine means the soak is about to adopt an Instance
// it does not own.
func soakPreexisting(ns string, before soakEngine) []string {
	var found []string
	for _, desc := range before.containers {
		if name, _, _ := strings.Cut(desc, " "); strings.HasPrefix(strings.TrimPrefix(name, "/"), ns+"-") {
			found = append(found, "container "+name)
		}
	}
	for volume := range before.volumes {
		if strings.HasPrefix(volume, ns+"_") {
			found = append(found, "volume "+volume)
		}
	}
	for tag := range before.tags {
		if strings.HasPrefix(tag, ns+":") {
			found = append(found, "image "+tag)
		}
	}
	sort.Strings(found)
	return found
}

func (s *soak) run() {
	s.phaseInit()
	s.phaseCycles()
	s.phaseStart()
	s.phaseRebuilds()
	s.phaseLongRunning()
	s.phaseObsoleteImages()
}

// phaseInit writes the soak checkout's contract and a tracked seed, and records
// the Instance namespace the Checkout Setup names.
func (s *soak) phaseInit() {
	if err := soakCheckRoot(s.env, s.dir, ""); err != nil {
		s.t.Fatal(err)
	}
	args := []string{"init", "--yes", "--json", "--name", "igdev-soak", "--gateway-memory-mb", strconv.Itoa(s.cfg.memoryMB)}
	if s.cfg.version != "" {
		args = append(args, "--ignition-version", s.cfg.version)
	}
	s.must("init", args...)
	if err := soakCheckRoot(s.env, s.dir, s.dir); err != nil {
		s.t.Fatal(err)
	}
	s.writeSeed(0)
	contractPath := filepath.Join(s.dir, project.ContractFile)
	body, err := os.ReadFile(contractPath)
	if err != nil {
		s.t.Fatal(err)
	}
	// [gateway] is the last table init renders, so the key lands in it.
	if err := os.WriteFile(contractPath, append(body, []byte("seed = [\"soak-seed\"]\n")...), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.setup("init")
	if s.cfg.version == "" {
		s.cfg.version = s.contractVersion()
	}
}

// setup materializes the Checkout Setup and records the Instance it names.
func (s *soak) setup(phase string) {
	s.must(phase, "setup", "--json")
	s.ns = setupStampOf(s.t, s.dir).Namespace()
	if s.ns == "" {
		s.t.Fatal("the Checkout Setup names no Instance")
	}
	if !slices.Contains(s.namespaces, s.ns) {
		if found := soakPreexisting(s.ns, s.before); len(found) > 0 {
			ns := s.ns
			s.ns = ""
			s.t.Fatalf("the soak's Setup names Instance %s, which was on the engine before the soak started (%s); "+
				"the soak only ever works on an Instance it minted", ns, strings.Join(found, ", "))
		}
		s.namespaces = append(s.namespaces, s.ns)
	}
}

// phaseCycles repeats up, wait, down, up from the kept volume, and cleanup with a
// fixed seed. Each cycle must end with nothing of the Instance on the engine.
func (s *soak) phaseCycles() {
	const phase = "cycles"
	for i := 1; i <= s.cfg.cycles; i++ {
		if !s.setupPresent() {
			s.setup(phase)
		}
		s.must(phase, "gateway", "up", "--json")
		s.must(phase, "gateway", "wait", "--timeout", s.waitTimeout(), "--json")
		s.sample(phase, fmt.Sprintf("cycle %d up", i), false)
		s.must(phase, "gateway", "down", "--json")
		if owned := s.owned(); len(owned.containers) != 0 || len(owned.volumes) != 1 {
			s.t.Fatalf("cycle %d: down left containers %v and volumes %v; want no container and the data volume", i, owned.containers, owned.volumes)
		}
		s.must(phase, "gateway", "up", "--json")
		s.must(phase, "gateway", "wait", "--timeout", s.waitTimeout(), "--json")
		s.cleanup(phase, false)
		if owned := s.owned(); owned.total() != 0 {
			s.t.Fatalf("cycle %d: cleanup left %s", i, owned)
		}
		if s.setupPresent() {
			s.t.Fatalf("cycle %d: .igdev/ survived cleanup", i)
		}
		s.sample(phase, fmt.Sprintf("cycle %d cleaned", i), true)
	}
	s.evidence = append(s.evidence, fmt.Sprintf("cycles: %d up/wait/down/up/cleanup cycles each left no container, volume, network or image of their Instance (%s) (#142, #145, #147)", s.cfg.cycles, strings.Join(s.namespaces, ", ")))
}

// phaseStart boots the Instance the remaining phases share. With
// IGDEV_SOAK_OLD_VERSION it first boots that version, stops it keeping its volume,
// moves the contract to the current version and boots that from the same volume:
// the upgrade a person makes, which leaves an obsolete Instance image behind for
// phaseObsoleteImages.
func (s *soak) phaseStart() {
	const phase = "start"
	if !s.setupPresent() {
		s.setup(phase)
	}
	if s.cfg.oldVersion != "" {
		s.must(phase, "init", "--yes", "--json", "--ignition-version", s.cfg.oldVersion)
		s.setup(phase)
		s.must(phase, "gateway", "up", "--json")
		s.must(phase, "gateway", "wait", "--timeout", s.waitTimeout(), "--json")
		s.sample(phase, "old version up", true)
		s.must(phase, "gateway", "down", "--json")
		s.must(phase, "init", "--yes", "--json", "--ignition-version", s.cfg.version)
		s.setup(phase)
	}
	s.must(phase, "gateway", "up", "--json")
	s.must(phase, "gateway", "wait", "--timeout", s.waitTimeout(), "--json")
	if s.cfg.oldVersion != "" && len(s.imageIDs(s.ns+":"+s.cfg.oldVersion)) == 0 {
		s.t.Fatalf("the %s image of %s is gone before cleanup --obsolete-images ran", s.cfg.oldVersion, s.ns)
	}
	s.sample(phase, "up", true)
}

// phaseRebuilds changes the seed and resets the Gateway, which rebuilds its image
// under the same tag each time; then a dry-run cleanup must plan every image the
// Instance owns, dangling rebuilds included (#145).
func (s *soak) phaseRebuilds() {
	const phase = "rebuilds"
	for i := 1; i <= s.cfg.rebuilds; i++ {
		s.writeSeed(i)
		s.setup(phase)
		res := s.must(phase, "gateway", "ensure", "--fresh", "--timeout", s.waitTimeout(), "--json")
		var data struct {
			Action string `json:"action"`
		}
		testrig.DataOf(s.t, res.Stdout, &data)
		if data.Action != "reset" {
			s.t.Fatalf("rebuild %d: ensure --fresh = %q, want reset", i, data.Action)
		}
		s.sample(phase, fmt.Sprintf("rebuild %d", i), true)
	}
	owned := s.owned()
	plan := s.cleanupPlan(phase, "cleanup", "--dry-run", "--json")
	planned := map[string]string{}
	for _, item := range plan {
		if item.Kind == "image" {
			planned[item.Ref] = item.Action
		}
	}
	for _, id := range owned.images {
		if _, ok := planned[id]; !ok {
			s.t.Fatalf("a dry-run cleanup does not list image %s of %s (owned %v, planned %v)", id, s.ns, owned.images, planned)
		}
	}
	s.evidence = append(s.evidence, fmt.Sprintf("rebuilds: %d seed-changing rebuilds left %d dangling image(s) of %s on this engine; a dry-run cleanup listed all %d owned image IDs (#145)",
		s.cfg.rebuilds, len(owned.dangling), s.ns, len(owned.images)))
}

// phaseLongRunning keeps the Gateway up for the configured duration and samples
// it at the interval: container memory, data-volume bytes, log bytes, caches,
// object counts, the trial, and the CLI's own peak memory per verb.
func (s *soak) phaseLongRunning() {
	const phase = "long-running"
	end := time.Now().Add(s.cfg.duration)
	if deadline, ok := s.t.Deadline(); ok && deadline.Add(-s.cfg.teardownAllowance).Before(end) {
		end = deadline.Add(-s.cfg.teardownAllowance)
		s.note("long-running phase shortened to end at %s to leave %s for teardown before the go test deadline", end.Format(time.RFC3339), s.cfg.teardownAllowance)
	}
	gatewayStarted := s.startedAt(s.ns + "-gateway-1")
	n := 0
	for {
		n++
		s.sample(phase, fmt.Sprintf("sample %d", n), true)
		s.checkRunning(phase)
		res := s.must(phase, "gateway", "ensure", "--json")
		var ensure struct {
			Action string `json:"action"`
		}
		testrig.DataOf(s.t, res.Stdout, &ensure)
		if ensure.Action != "reused" {
			s.t.Fatalf("sample %d: ensure on a healthy Gateway = %q, want reused", n, ensure.Action)
		}
		if time.Now().Add(s.cfg.interval).After(end) {
			break
		}
		s.sleep(s.cfg.interval)
	}
	if after := s.startedAt(s.ns + "-gateway-1"); after != gatewayStarted {
		s.t.Fatalf("the Gateway restarted during the long-running phase: started %s, then %s", gatewayStarted, after)
	}
	s.evidence = append(s.evidence, fmt.Sprintf("long-running: %d samples over %s; the Gateway stayed running without a restart, the keeper ran beside it, and every ensure reused it (#147)",
		n, time.Since(s.samples[len(s.samples)-n].At).Round(time.Second)))
}

// phaseObsoleteImages reclaims the old-version images on the real engine (#153):
// a dry run plans them, the run removes them by ID, and the running Instance, its
// volume, its current image and its Checkout Setup stay.
func (s *soak) phaseObsoleteImages() {
	const phase = "obsolete-images"
	if s.cfg.oldVersion == "" {
		s.note("cleanup --obsolete-images execution was not exercised: IGDEV_SOAK_OLD_VERSION is unset")
		return
	}
	owned := s.owned()
	ownedSet := map[string]bool{}
	for _, id := range owned.images {
		ownedSet[id] = true
	}
	plan := s.cleanupPlan(phase, "cleanup", "--obsolete-images", "--dry-run", "--json")
	oldIDs := s.imageIDs(s.ns + ":" + s.cfg.oldVersion)
	officialOld := "inductiveautomation/ignition:" + s.cfg.oldVersion
	plannedOfficial := false
	for _, item := range plan {
		switch item.Kind {
		case "image", "base_image":
		default:
			s.t.Fatalf("--obsolete-images planned a %s: %s", item.Kind, item.Ref)
		}
		if item.Action != "would_remove" {
			continue
		}
		if item.Kind == "image" && !ownedSet[item.Ref] {
			s.t.Fatalf("--obsolete-images plans image %s, which %s does not own", item.Ref, s.ns)
		}
		if item.Kind == "base_image" {
			if s.before.tags[item.Ref] {
				// An official image that was here before the soak is not the soak's to
				// remove, however conservative igdev's rule is: stop before acting.
				s.note("cleanup --obsolete-images execution was not exercised: the plan removes %s, which was on the engine before the soak", item.Ref)
				s.t.Logf("skipping --obsolete-images execution: it would remove pre-existing %s", item.Ref)
				return
			}
			plannedOfficial = plannedOfficial || item.Ref == officialOld
		}
	}
	for _, id := range oldIDs {
		if !slices.ContainsFunc(plan, func(item soakCleanupItem) bool { return item.Ref == id && item.Action == "would_remove" }) {
			s.t.Fatalf("--obsolete-images --dry-run does not plan the %s image %s: %+v", s.cfg.oldVersion, id, plan)
		}
	}
	gateway := s.ns + "-gateway-1"
	started := s.startedAt(gateway)
	current := s.imageIDs(s.ns + ":" + s.cfg.version)
	// A BuildKit build pulls the base into its cache without tagging an official
	// image, so there may be no official image of the old version to reclaim.
	officialBefore := s.tagExists(officialOld)

	items := s.cleanup(phase, true, "--obsolete-images")
	if again := s.cleanupPlan(phase, "cleanup", "--obsolete-images", "--dry-run", "--json"); slices.ContainsFunc(again, func(item soakCleanupItem) bool { return item.Action == "would_remove" }) {
		s.t.Fatalf("a second --obsolete-images dry run still plans work: %+v", again)
	}
	for _, id := range oldIDs {
		if s.imageExists(id) {
			s.t.Fatalf("the %s image %s survived cleanup --obsolete-images", s.cfg.oldVersion, id)
		}
	}
	if dangling := s.owned().dangling; len(dangling) != 0 {
		s.t.Fatalf("dangling images of %s survived cleanup --obsolete-images: %v", s.ns, dangling)
	}
	if got := s.imageIDs(s.ns + ":" + s.cfg.version); strings.Join(got, ",") != strings.Join(current, ",") {
		s.t.Fatalf("the current image changed: %v, was %v", got, current)
	}
	if after := s.startedAt(gateway); after != started {
		s.t.Fatalf("cleanup --obsolete-images restarted the Gateway: started %s, then %s", started, after)
	}
	if !s.setupPresent() || len(s.owned().volumes) != 1 {
		s.t.Fatal("cleanup --obsolete-images removed the Checkout Setup or the data volume")
	}
	s.checkRunning(phase)
	officialAfter := s.tagExists(officialOld)
	switch {
	case plannedOfficial && officialAfter:
		s.t.Fatalf("%s was planned but survived cleanup --obsolete-images", officialOld)
	case !plannedOfficial && officialBefore != officialAfter:
		s.t.Fatalf("%s was not planned but changed: present %v, then %v", officialOld, officialBefore, officialAfter)
	}
	removed := 0
	for _, item := range items {
		if item.Action == "removed" {
			removed++
		}
	}
	official := ", and removed " + officialOld
	switch {
	case !officialBefore:
		official = "; no " + officialOld + " image was tagged on this engine (the build pulled its base into the build cache), so the official-image rule had nothing to act on"
	case !plannedOfficial:
		official = "; it kept " + officialOld + " (still referenced, or not attributable to this Instance)"
	}
	s.evidence = append(s.evidence, fmt.Sprintf("obsolete-images: the real engine removed %d item(s) by ID: the %s Instance image%s. The running Gateway (same StartedAt), its volume, its %s image and .igdev/ stayed, and a second dry run planned nothing (#153)",
		removed, s.cfg.oldVersion, official, s.cfg.version))
	s.sample(phase, "after --obsolete-images", true)
}

// teardown runs whatever happened: diagnostics on failure, then the checkout-scope
// cleanup of the soak's own Instance, the leftover and ownership checks, and the
// report.
func (s *soak) teardown() {
	t := s.t
	// The report is written however the teardown ends; leftovers stays nil when
	// a failure stopped it before the engine was checked.
	var leftovers *soakOwned
	defer func() {
		signal.Stop(s.signal)
		s.writeReport(leftovers)
		_ = s.commands.Close()
		if leftovers != nil && leftovers.total() == 0 && !t.Failed() {
			_ = os.RemoveAll(s.dir)
		}
		t.Logf("soak report: %s", filepath.Join(s.cfg.reportDir, "summary.md"))
	}()
	if t.Failed() && s.setupPresent() {
		s.diagnostics()
	}
	if len(s.namespaces) == 0 {
		leftovers = &soakOwned{}
		return
	}
	if _, err := os.Stat(s.dir); err == nil {
		s.cleanup("teardown", false)
	}
	left := soakOwned{}
	for _, ns := range s.namespaces {
		left = left.plus(s.ownedBy(ns))
	}
	leftovers = &left
	if left.total() != 0 {
		t.Errorf("the soak left %s; remove them with: cd %s && igdev cleanup", left, s.dir)
	}
	s.checkDoctor()
}

// cleanup runs a checkout-scope cleanup and checks that it removed only what the
// soak owns.
func (s *soak) cleanup(phase string, mustSucceed bool, extra ...string) []soakCleanupItem {
	ownedSet := map[string]bool{}
	for _, ns := range s.namespaces {
		owned := s.ownedBy(ns)
		for _, list := range [][]string{owned.images, owned.containers} {
			for _, id := range list {
				ownedSet[id] = true
			}
		}
	}
	args := append([]string{"cleanup"}, extra...)
	args = append(args, "--json")
	res := s.igdev(phase, args...)
	s.keep(phase, "cleanup", res.Stdout)
	if res.Exit != int(contract.ExitOK) && (mustSucceed || phase != "teardown") {
		s.t.Fatalf("%s: igdev %s: exit %d\n%s", phase, strings.Join(args, " "), res.Exit, soakTail(res.Stdout+res.Stderr))
	}
	if res.Exit != int(contract.ExitOK) {
		s.t.Errorf("teardown cleanup: exit %d\n%s", res.Exit, soakTail(res.Stdout+res.Stderr))
	}
	var data struct {
		Scope string            `json:"scope"`
		Items []soakCleanupItem `json:"items"`
	}
	if err := soakData(res.Stdout, &data); err != nil {
		s.t.Errorf("%s: cleanup printed no readable envelope: %v", phase, err)
		return nil
	}
	if data.Scope != "checkout" {
		s.t.Fatalf("%s: cleanup ran with scope %q; the soak only ever cleans its checkout", phase, data.Scope)
	}
	if foreign := soakForeignRemovals(s.namespaces, data.Items, ownedSet, s.before.tags, s.dir); len(foreign) > 0 {
		s.t.Fatalf("%s: cleanup removed what the soak does not own: %v", phase, foreign)
	}
	return data.Items
}

// cleanupPlan runs a dry-run cleanup and returns its items.
func (s *soak) cleanupPlan(phase string, args ...string) []soakCleanupItem {
	res := s.must(phase, args...)
	s.keep(phase, "plan", res.Stdout)
	var data struct {
		DryRun bool              `json:"dry_run"`
		Items  []soakCleanupItem `json:"items"`
	}
	testrig.DataOf(s.t, res.Stdout, &data)
	if !data.DryRun {
		s.t.Fatalf("%s: %v did not report a dry run", phase, args)
	}
	return data.Items
}

// checkRunning asserts the Gateway reports running with its trial keeper beside it.
func (s *soak) checkRunning(phase string) {
	res := s.must(phase, "gateway", "status", "--json")
	var status struct {
		State    string `json:"state"`
		Services []struct {
			Service string `json:"service"`
			State   string `json:"state"`
		} `json:"services"`
	}
	testrig.DataOf(s.t, res.Stdout, &status)
	keeper := ""
	for _, service := range status.Services {
		if service.Service == "trial-keeper" {
			keeper = service.State
		}
	}
	if status.State != "running" || keeper != "running" {
		s.t.Fatalf("%s: gateway status = %q, keeper %q; want both running", phase, status.State, keeper)
	}
}

// checkDoctor asserts the read-only inventory (#152) no longer lists the soak's
// Instance once its teardown ran.
func (s *soak) checkDoctor() {
	res := s.igdev("teardown", "doctor", "--json")
	var data struct {
		Instances struct {
			Engine string `json:"engine"`
			Items  []struct {
				Namespace string `json:"namespace"`
			} `json:"items"`
		} `json:"instances"`
	}
	if err := soakData(res.Stdout, &data); err != nil {
		s.t.Errorf("doctor printed no readable envelope: %v", err)
		return
	}
	for _, item := range data.Instances.Items {
		if slices.Contains(s.namespaces, item.Namespace) {
			s.t.Errorf("doctor still lists the soak's Instance %s after teardown", item.Namespace)
		}
	}
}

// sample records one reading. Engine-wide measurements that are slow on a busy
// host (the build cache) are taken only at phase boundaries.
func (s *soak) sample(phase, label string, boundary bool) {
	metrics := map[string]float64{}
	owned := s.owned()
	metrics["owned_containers"] = float64(len(owned.containers))
	metrics["owned_volumes"] = float64(len(owned.volumes))
	metrics["owned_networks"] = float64(len(owned.networks))
	metrics["owned_images"] = float64(len(owned.images))
	metrics["owned_dangling_images"] = float64(len(owned.dangling))
	metrics["cache_bytes"] = float64(soakTreeBytes(filepath.Join(s.env.Home, ".cache", "igdev")))
	metrics["state_bytes"] = float64(soakTreeBytes(filepath.Join(s.env.Home, ".local", "state", "igdev")))
	if entries, err := os.ReadDir(s.env.Temp); err == nil {
		metrics["tmp_entries"] = float64(len(entries))
	}
	if boundary {
		if size, ok := s.buildCacheBytes(); ok {
			metrics["build_cache_bytes"] = size
		}
	}
	gateway := s.ns + "-gateway-1"
	if slices.Contains(owned.names, gateway) && s.running(gateway) {
		for name, container := range map[string]string{"gateway": gateway, "keeper": s.ns + "-trial-keeper-1"} {
			if rss, ok := s.containerMemory(container); ok {
				metrics[name+"_mem_bytes"] = rss
			}
		}
		if n, err := s.logBytes(gateway); err == nil {
			metrics["gateway_log_bytes"] = float64(n)
		}
		if res := s.igdev(phase, "gateway", "exec", "--", "du", "-sb", soakContainerFS); res.Exit == 0 {
			if fields := strings.Fields(res.Stdout); len(fields) > 0 {
				if n, err := strconv.ParseFloat(fields[0], 64); err == nil {
					metrics["volume_bytes"] = n
				}
			}
		}
		res := s.igdev(phase, "gateway", "trial", "--json")
		var trial struct {
			Trial *struct {
				SecondsLeft float64 `json:"seconds_left"`
				Expired     bool    `json:"expired"`
			} `json:"trial"`
		}
		if res.Exit == 0 && soakData(res.Stdout, &trial) == nil && trial.Trial != nil {
			metrics["trial_seconds_left"] = trial.Trial.SecondsLeft
		}
		// The CLI's own peak memory while the log grows (#143): the human stream
		// and the bounded tail.
		for name, args := range map[string][]string{
			"cli_rss_kb_logs_stream": {"gateway", "logs"},
			"cli_rss_kb_logs_tail":   {"gateway", "logs", "--tail", "200"},
			"cli_rss_kb_status":      {"gateway", "status", "--json"},
		} {
			if res := s.igdev(phase, args...); res.Exit == 0 && res.PeakRSSKB > 0 {
				metrics[name] = float64(res.PeakRSSKB)
				if name == "cli_rss_kb_logs_stream" {
					metrics["logs_stream_bytes"] = float64(len(res.Stdout))
				}
			}
		}
	}
	sample := soakSample{At: time.Now().UTC(), Phase: phase, Label: label, Metrics: metrics}
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	s.mu.Unlock()
	if raw, err := json.Marshal(sample); err == nil {
		s.appendFile("samples.jsonl", append(raw, '\n'))
	}
}

// igdev runs one verb in the soak checkout, recording its exit, duration and peak
// memory. It stops the soak, before starting anything, once a signal arrived.
func (s *soak) igdev(phase string, args ...string) testrig.Result {
	s.t.Helper()
	if phase != "teardown" {
		s.checkSignal()
	}
	res := s.env.Run(testrig.Run{Args: args, Dir: s.dir, SampleRSS: true, RSSInterval: 20 * time.Millisecond})
	if res.Err != nil {
		s.t.Fatalf("igdev %s: %v", strings.Join(args, " "), res.Err)
	}
	row, _ := json.Marshal(map[string]any{
		"at": time.Now().UTC(), "phase": phase, "args": args, "exit": res.Exit,
		"duration_ms": res.Duration.Milliseconds(), "peak_rss_kb": res.PeakRSSKB,
		"stdout_bytes": len(res.Stdout), "stderr_bytes": len(res.Stderr),
	})
	if _, err := s.commands.Write(append(row, '\n')); err != nil {
		s.t.Logf("command log: %v", err)
	}
	return res
}

// must runs a verb that has to succeed. A consent refusal stops the soak with the
// person's step: the soak never accepts anything itself.
func (s *soak) must(phase string, args ...string) testrig.Result {
	s.t.Helper()
	res := s.igdev(phase, args...)
	if res.Exit == int(contract.ExitHumanAction) && strings.Contains(res.Stdout, string(contract.CodeConsentRequired)) {
		s.t.Fatalf("%s: igdev %s: IGDEV_E_CONSENT_REQUIRED. Consent is human-only: a person runs `igdev setup --accept-eula` "+
			"for this machine; the soak stops here and never accepts it", phase, strings.Join(args, " "))
	}
	if res.Exit != int(contract.ExitOK) {
		s.t.Fatalf("%s: igdev %s: exit %d\n%s", phase, strings.Join(args, " "), res.Exit, soakTail(res.Stdout+res.Stderr))
	}
	return res
}

func (s *soak) checkSignal() {
	if s.stopped == nil {
		select {
		case sig := <-s.signal:
			s.stopped = sig
		default:
		}
	}
	if s.stopped != nil {
		s.t.Fatalf("interrupted by %v: stopping before the next step and removing only %v", s.stopped, s.namespaces)
	}
}

// sleep waits for d, waking early on a signal.
func (s *soak) sleep(d time.Duration) {
	select {
	case sig := <-s.signal:
		s.stopped = sig
		s.checkSignal()
	case <-time.After(d):
	}
}

// diagnostics keeps what a person needs to read a failure, before teardown
// removes the Instance.
func (s *soak) diagnostics() {
	dir := filepath.Join(s.cfg.reportDir, "diagnostics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	for name, args := range map[string][]string{
		"gateway-status.json": {"gateway", "status", "--json"},
		"gateway-logs.txt":    {"gateway", "logs", "--tail", "500"},
		"doctor.json":         {"doctor", "--json"},
		"cleanup-plan.json":   {"cleanup", "--dry-run", "--json"},
	} {
		res := s.igdev("teardown", args...)
		_ = os.WriteFile(filepath.Join(dir, name), []byte(res.Stdout+res.Stderr), 0o644)
	}
	var rows []string
	for _, ns := range s.namespaces {
		if out, err := s.docker("ps", "--all", "--filter", "label="+soakComposeKey+"="+ns, "--format", "{{.Names}}\t{{.Status}}"); err == nil {
			rows = append(rows, out)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "containers.txt"), []byte(strings.Join(rows, "")), 0o644)
}

// writeSeed writes the tracked seed: one tag provider whose resource changes with
// n, so every new n changes the Setup Stamp's seed digest and rebuilds the image.
func (s *soak) writeSeed(n int) {
	dir := filepath.Join(s.dir, "soak-seed", "ignition", "tag-provider", "igdev-soak")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	resource := fmt.Sprintf(`{"scope":"A","version":1,"restricted":false,"overridable":true,"files":["config.json"],"attributes":{"uuid":"6a0d2c1e-4f6b-4c55-9a2e-%012x"}}`+"\n", n)
	config := `{"profile":{"type":"STANDARD"},"settings":{"readOnly":false,"valuePersistence":"Database"}}` + "\n"
	for name, body := range map[string]string{"resource.json": resource, "config.json": config} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *soak) setupPresent() bool {
	_, err := os.Stat(filepath.Join(s.dir, project.StateDir, project.SetupRecord))
	return err == nil
}

func (s *soak) contractVersion() string {
	res := s.must("init", "status", "--json")
	if v, ok := testrig.Status(s.t, res.Stdout).ResolvedValue("ignition.version").(string); ok && v != "" {
		return v
	}
	s.t.Fatal("status reports no ignition.version")
	return ""
}

func (s *soak) waitTimeout() string {
	return strconv.Itoa(int(s.cfg.waitTimeout.Seconds())) + "s"
}

func (s *soak) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

// keep saves a cleanup envelope under envelopes/ in the report, numbered in run
// order, so the plan behind every assertion can be read afterwards.
func (s *soak) keep(phase, kind, stdout string) {
	dir := filepath.Join(s.cfg.reportDir, "envelopes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	s.kept++
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d-%s-%s.json", s.kept, phase, kind)), []byte(stdout), 0o644)
}

func (s *soak) appendFile(name string, data []byte) {
	f, err := os.OpenFile(filepath.Join(s.cfg.reportDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}

// soakDockerReadOnly lists the docker verbs the soak itself may run: inspection
// only. Every change to the engine goes through an igdev verb.
var soakDockerReadOnly = map[string][]string{
	"ps": nil, "inspect": nil, "stats": nil, "logs": nil, "info": nil, "version": nil,
	"context":   {"inspect", "show"},
	"image":     {"ls", "inspect"},
	"volume":    {"ls", "inspect"},
	"network":   {"ls", "inspect"},
	"container": {"ls", "inspect"},
	"system":    {"df"},
	"compose":   {"version"},
}

// docker runs a read-only docker command against the soak's engine.
func (s *soak) docker(args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("no docker verb")
	}
	subs, ok := soakDockerReadOnly[args[0]]
	if !ok || (subs != nil && (len(args) < 2 || !slices.Contains(subs, args[1]))) {
		s.t.Fatalf("the soak only reads the engine; refusing docker %s", strings.Join(args, " "))
	}
	cmd := exec.Command("docker", args...)
	cmd.Env = os.Environ()
	if s.cfg.dockerHost != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+s.cfg.dockerHost)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return string(out), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return string(out), fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (s *soak) lines(args ...string) []string {
	out, err := s.docker(args...)
	if err != nil {
		s.t.Fatalf("%v", err)
	}
	seen := map[string]bool{}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines
}

// soakOwned is every engine object attributed to the soak's Instance by its
// Compose project label or its igdev-<id8> image name, queried independently of
// igdev's own inventory.
type soakOwned struct {
	containers, names, volumes, networks, images, dangling []string
}

func (o soakOwned) total() int {
	return len(o.containers) + len(o.volumes) + len(o.networks) + len(o.images)
}

func (o soakOwned) String() string {
	return fmt.Sprintf("containers %v, volumes %v, networks %v, images %v (dangling %v)", o.names, o.volumes, o.networks, o.images, o.dangling)
}

func (o soakOwned) plus(other soakOwned) soakOwned {
	return soakOwned{
		containers: append(o.containers, other.containers...), names: append(o.names, other.names...),
		volumes: append(o.volumes, other.volumes...), networks: append(o.networks, other.networks...),
		images: append(o.images, other.images...), dangling: append(o.dangling, other.dangling...),
	}
}

// owned is what the current Instance holds on the engine.
func (s *soak) owned() soakOwned { return s.ownedBy(s.ns) }

func (s *soak) ownedBy(ns string) soakOwned {
	label := "label=" + soakComposeKey + "=" + ns
	images := s.lines("image", "ls", "--all", "--quiet", "--no-trunc", "--filter", label)
	images = append(images, s.lines("image", "ls", "--all", "--quiet", "--no-trunc", "--filter", "reference="+ns)...)
	slices.Sort(images)
	return soakOwned{
		containers: s.lines("ps", "--all", "--quiet", "--no-trunc", "--filter", label),
		names:      s.lines("ps", "--all", "--format", "{{.Names}}", "--filter", label),
		volumes:    s.lines("volume", "ls", "--quiet", "--filter", label),
		networks:   s.lines("network", "ls", "--quiet", "--no-trunc", "--filter", label),
		images:     slices.Compact(images),
		dangling:   s.lines("image", "ls", "--all", "--quiet", "--no-trunc", "--filter", "dangling=true", "--filter", label),
	}
}

// soakEngine is the engine as the soak found it.
type soakEngine struct {
	containers map[string]string // ID -> name, status and start time
	volumes    map[string]bool
	networks   map[string]bool
	images     map[string]bool
	tags       map[string]bool
}

func (s *soak) engine() soakEngine {
	e := soakEngine{containers: map[string]string{}, volumes: map[string]bool{}, networks: map[string]bool{}, images: map[string]bool{}, tags: map[string]bool{}}
	for _, id := range s.lines("ps", "--all", "--quiet", "--no-trunc") {
		if out, err := s.docker("container", "inspect", "--format", "{{.Name}} {{.State.Status}} {{.State.StartedAt}}", id); err == nil {
			e.containers[id] = strings.TrimSpace(out)
		}
	}
	for _, v := range s.lines("volume", "ls", "--quiet") {
		e.volumes[v] = true
	}
	for _, n := range s.lines("network", "ls", "--quiet", "--no-trunc") {
		e.networks[n] = true
	}
	for _, row := range s.lines("image", "ls", "--all", "--no-trunc", "--format", "{{.ID}}\t{{.Repository}}:{{.Tag}}") {
		id, tag, _ := strings.Cut(row, "\t")
		e.images[id] = true
		if !strings.Contains(tag, "<none>") {
			e.tags[tag] = true
		}
	}
	return e
}

// changesSince lists the pre-existing objects that are gone or changed. Other
// actors on a shared host can account for these, so they are reported, while the
// soak's own guarantee is soakForeignRemovals over every cleanup it ran.
func (s *soak) changesSince(before soakEngine) []string {
	now := s.engine()
	var changes []string
	for id, was := range before.containers {
		if is, ok := now.containers[id]; !ok {
			changes = append(changes, "container gone: "+was)
		} else if is != was {
			changes = append(changes, fmt.Sprintf("container changed: %s -> %s", was, is))
		}
	}
	for kind, pair := range map[string][2]map[string]bool{
		"volume": {before.volumes, now.volumes}, "network": {before.networks, now.networks},
		"image": {before.images, now.images}, "tag": {before.tags, now.tags},
	} {
		for ref := range pair[0] {
			if !pair[1][ref] {
				changes = append(changes, kind+" gone: "+ref)
			}
		}
	}
	sort.Strings(changes)
	return changes
}

func (s *soak) imageIDs(ref string) []string {
	return s.lines("image", "ls", "--all", "--quiet", "--no-trunc", ref)
}

func (s *soak) imageExists(id string) bool {
	_, err := s.docker("image", "inspect", "--format", "{{.Id}}", id)
	return err == nil
}

func (s *soak) tagExists(ref string) bool {
	return len(s.imageIDs(ref)) > 0
}

func (s *soak) startedAt(container string) string {
	out, err := s.docker("container", "inspect", "--format", "{{.State.StartedAt}}", container)
	if err != nil {
		s.t.Fatalf("%v", err)
	}
	return strings.TrimSpace(out)
}

func (s *soak) running(container string) bool {
	out, err := s.docker("container", "inspect", "--format", "{{.State.Running}}", container)
	return err == nil && strings.TrimSpace(out) == "true"
}

// containerMemory reads the container's current memory use from docker stats.
func (s *soak) containerMemory(container string) (float64, bool) {
	out, err := s.docker("stats", "--no-stream", "--format", "{{.MemUsage}}", container)
	if err != nil {
		return 0, false
	}
	used, _, _ := strings.Cut(strings.TrimSpace(out), "/")
	size, err := parseDockerSize(used)
	return size, err == nil
}

// logBytes counts the bytes of the container's log without keeping them.
func (s *soak) logBytes(container string) (int64, error) {
	cmd := exec.Command("docker", "logs", container)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+s.cfg.dockerHost)
	var count soakCounter
	cmd.Stdout, cmd.Stderr = &count, &count
	err := cmd.Run()
	return int64(count), err
}

type soakCounter int64

func (c *soakCounter) Write(p []byte) (int, error) {
	*c += soakCounter(len(p))
	return len(p), nil
}

// buildCacheBytes reads the engine's build-cache size: shared, never pruned by
// igdev or the soak, and reported so its growth across rebuilds is visible.
func (s *soak) buildCacheBytes() (float64, bool) {
	out, err := s.docker("system", "df", "--format", "{{.Type}}\t{{.Size}}")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(out, "\n") {
		if kind, size, ok := strings.Cut(line, "\t"); ok && kind == "Build Cache" {
			value, err := parseDockerSize(size)
			return value, err == nil
		}
	}
	return 0, false
}

func soakTreeBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// soakData decodes an envelope's data without failing the test, for the steps
// whose output is a measurement rather than an assertion.
func soakData(stdout string, target any) error {
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &probe); err != nil {
		return err
	}
	if len(probe.Data) == 0 || string(probe.Data) == "null" {
		return errors.New("the envelope carries no data")
	}
	return json.Unmarshal(probe.Data, target)
}

func soakTail(s string) string {
	const limit = 4096
	if len(s) > limit {
		return "…" + s[len(s)-limit:]
	}
	return s
}

// writeReport writes summary.md: environment, workload, evidence, trends and what
// changed on the host, next to samples.jsonl and commands.jsonl.
func (s *soak) writeReport(leftovers *soakOwned) {
	var b strings.Builder
	result := "PASS"
	if s.t.Failed() {
		result = "FAIL"
	}
	fmt.Fprintf(&b, "# igdev real-Gateway soak\n\nResult: **%s**. Started %s, ran %s.\n\n", result, s.started.UTC().Format(time.RFC3339), time.Since(s.started).Round(time.Second))
	b.WriteString("## Environment\n\n")
	server, _ := s.docker("version", "--format", "{{.Server.Version}}")
	// The storage backend decides whether a rebuild leaves its old image dangling
	// (the containerd image store drops it), so it belongs with the evidence.
	storage, _ := s.docker("info", "--format", "{{.Driver}} {{range .DriverStatus}}{{index . 1}} {{end}}")
	compose, _ := s.docker("compose", "version", "--short")
	commit := "unknown"
	if root, err := testrig.RepoRoot(); err == nil {
		if out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output(); err == nil {
			commit = strings.TrimSpace(string(out))
		}
	}
	mem := "unknown"
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		mem = strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	}
	fmt.Fprintf(&b, "- igdev: built from %s (%s)\n- Docker engine %s (storage %s), Compose %s, endpoint %s\n- Host: %s/%s, %d CPUs, %s\n- Instances: %s, checkout %s\n- Consent read from %s (read-only)\n\n",
		commit, runtime.Version(), strings.TrimSpace(server), strings.TrimSpace(storage), strings.TrimSpace(compose), s.cfg.dockerHost,
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), mem, strings.Join(s.namespaces, ", "), s.dir, s.cfg.consent)
	b.WriteString("## Workload\n\n")
	old := s.cfg.oldVersion
	if old == "" {
		old = "unset (no version change; --obsolete-images execution not exercised)"
	}
	fmt.Fprintf(&b, "- Ignition %s; old version %s\n- %d up/down/cleanup cycles with a fixed seed; %d rebuilds with a changing seed\n- long-running phase %s, sampled every %s; wait timeout %s; Gateway heap %d MiB\n\n",
		s.cfg.version, old, s.cfg.cycles, s.cfg.rebuilds, s.cfg.duration, s.cfg.interval, s.cfg.waitTimeout, s.cfg.memoryMB)
	b.WriteString("## Evidence\n\n")
	for _, line := range s.evidence {
		b.WriteString("- " + line + "\n")
	}
	for _, line := range s.notes {
		b.WriteString("- Note: " + line + "\n")
	}
	switch {
	case leftovers == nil:
		fmt.Fprintf(&b, "- Teardown: **stopped before the engine was checked**; finish with `cd %s && igdev cleanup`, then `igdev doctor`\n", s.dir)
	case leftovers.total() == 0:
		fmt.Fprintf(&b, "- Teardown: nothing of %s is left on the engine, and doctor no longer lists them\n", strings.Join(s.namespaces, ", "))
	default:
		fmt.Fprintf(&b, "- Teardown: **left %s**; remove with `cd %s && igdev cleanup`\n", leftovers, s.dir)
	}
	b.WriteString("\n## Trends\n\nObservations, not verdicts: a rising series is a trend to read, never proof of a Gateway-internal leak.\n\n")
	b.WriteString(soakTrendTable(soakTrends(s.samples)))
	b.WriteString("\n## Pre-existing objects that changed during the soak\n\n")
	b.WriteString("Every cleanup the soak ran was checked to remove only its own objects; changes listed here come from other actors on the host.\n\n")
	if changes := s.changesSince(s.before); len(changes) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, change := range changes {
			b.WriteString("- " + change + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(s.cfg.reportDir, "summary.md"), []byte(b.String()), 0o644); err != nil {
		s.t.Errorf("write the soak report: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(s.cfg.reportDir, "summary.md")); err == nil {
		_, _ = io.WriteString(os.Stdout, string(data))
	}
}
