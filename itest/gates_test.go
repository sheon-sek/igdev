package itest

import (
	"strings"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// These are the resource and hygiene gates from the spec, asserted through seam
// S1 so "lightweight" stays a tested property instead of an intention. The
// thresholds are the frozen numbers: non-docker startup P95 under 100 ms, peak
// RSS under 64 MB, binary under 30 MB, zero temp-file leaks, zero orphan docker
// objects.
const (
	startupP95Limit = 100 * time.Millisecond
	peakRSSLimitKB  = 64 * 1024 // KiB
	binaryLimit     = 30 << 20  // bytes
	startupRuns     = 30
	rssRuns         = 10
)

// startupP95 is a shared, meaningful measurement: run the safe first call in a
// project fixture with the notifier off, so nothing but process start-up,
// discovery, and config resolution is on the clock.
func TestGateNonDockerStartupP95(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	root := env.Project("repo", testrig.MinimalContract)

	durations := make([]time.Duration, 0, startupRuns)
	for range startupRuns {
		res := env.RunIn(root, "status", "--json")
		testrig.WantExit(t, res, contract.ExitOK)
		durations = append(durations, res.Duration)
	}
	p95 := testrig.Percentile(durations, 95)
	fastest, slowest := testrig.Percentile(durations, 0), testrig.Percentile(durations, 100)
	t.Logf("startup over %d runs: min %v, p95 %v, max %v (limit %v)",
		startupRuns, fastest.Round(time.Millisecond), p95.Round(time.Millisecond),
		slowest.Round(time.Millisecond), startupP95Limit)
	if p95 >= startupP95Limit {
		t.Errorf("non-docker startup p95 = %v, want under %v", p95, startupP95Limit)
	}
	env.AssertNoDockerCalls(t)
}

// Peak RSS is read from the kernel's own high-water mark for the child process,
// not estimated.
func TestGatePeakRSS(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	root := env.Project("repo", testrig.MinimalContract)

	var peak int64
	samples := 0
	for range rssRuns {
		res := env.Run(testrig.Run{
			Args:      []string{"status", "--json"},
			Dir:       root,
			SampleRSS: true,
		})
		testrig.WantExit(t, res, contract.ExitOK)
		if res.Err != nil {
			t.Fatalf("run: %v", res.Err)
		}
		samples += res.Samples
		if res.PeakRSSKB > peak {
			peak = res.PeakRSSKB
		}
	}
	if samples == 0 {
		t.Fatalf("no /proc samples caught; the gate cannot be evaluated")
	}
	t.Logf("peak RSS across %d runs: %d KiB (%.1f MiB) from %d samples, limit %d KiB",
		rssRuns, peak, float64(peak)/1024, samples, peakRSSLimitKB)
	if peak >= peakRSSLimitKB {
		t.Errorf("peak RSS = %d KiB, want under %d KiB", peak, peakRSSLimitKB)
	}
}

// The shipped binary stays small enough to install over a hotel wifi.
func TestGateBinarySize(t *testing.T) {
	size := testrig.BinarySize(t)
	t.Logf("igdev binary: %d bytes (%.1f MiB), limit %d bytes", size, float64(size)/(1<<20), binaryLimit)
	if size >= binaryLimit {
		t.Errorf("binary size = %d bytes, want under %d", size, binaryLimit)
	}
}

// Every command in this ticket leaves the scratch tree exactly as it found it,
// and never creates a docker object.
func TestGateZeroLeaksAndOrphansAcrossCommands(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	root := env.Project("repo", testrig.MinimalContract)

	for _, args := range [][]string{
		{"status", "--json"},
		{"status"},
		{"version", "--json"},
		{"version"},
		{"help"},
		{"help", "status"},
		{"completion", "bash"},
		{"stat", "--json"},               // failure paths must clean up too
		{"status", "--nope"},             // and usage errors
		{"baseline", "status", "--json"}, // a Baseline needs a materialized checkout
		{"baseline", "set"},              // and names its argument
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := env.RunIn(root, args...)
			res.AssertNoLeaks(t)
		})
	}
	env.AssertNoDockerOrphans(t)
	env.AssertNoDockerCalls(t)
}

// The orphan tally is the assertion every later ticket reuses, so it is pinned
// here: identities, not counts. Creating one object and removing another leaves
// the first one orphaned.
func TestGateDockerOrphanTally(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()

	if got := env.DockerOrphans(t); got.Total() != 0 {
		t.Fatalf("a fresh environment reports orphans: %+v", got)
	}
	scripts := []string{
		`docker run --name igdev-a -d alpine`,
		`docker run --name igdev-b -d alpine`,
		`docker rm igdev-a`,
		`docker network create igdev-net`,
		`docker network rm igdev-net`,
		`docker volume create igdev-store`,
	}
	for _, script := range scripts {
		if res := env.RunShell(script); res.Exit != 0 {
			t.Fatalf("%s: %s", script, res.Stderr)
		}
	}
	got := env.DockerOrphans(t)
	if strings.Join(got.Containers, ",") != "igdev-b" {
		t.Errorf("orphaned containers = %v, want [igdev-b]", got.Containers)
	}
	if len(got.Networks) != 0 {
		t.Errorf("orphaned networks = %v, want none", got.Networks)
	}
	if strings.Join(got.Volumes, ",") != "igdev-store" {
		t.Errorf("orphaned volumes = %v, want [igdev-store]", got.Volumes)
	}
	if calls := env.DockerCalls(t); len(calls) != len(scripts) {
		t.Errorf("shim recorded %d calls, want %d", len(calls), len(scripts))
	}
	// The recorded argv is what the CLI actually asked for.
	last := env.DockerCalls(t)[len(scripts)-1]
	if joined := strings.Join(last.Argv, " "); joined != "volume create igdev-store" {
		t.Errorf("recorded argv = %v", last.Argv)
	}

	// A cleanup naming an object this run never created retires nothing and is
	// reported, so a typo cannot silently balance the books.
	env.RunShell(`docker rm igdev-never-existed`)
	got = env.DockerOrphans(t)
	if strings.Join(got.Containers, ",") != "igdev-b" {
		t.Errorf("an unmatched rm cleared the orphan: %v", got.Containers)
	}
	if len(got.Unmatched) != 1 || !strings.Contains(got.Unmatched[0], "igdev-never-existed") {
		t.Errorf("unmatched removal not reported: %v", got.Unmatched)
	}

	// `run --rm` is cleaned by the engine, so it is never an orphan.
	env.RunShell(`docker run --rm --name ephemeral alpine echo hi`)
	if strings.Join(env.DockerOrphans(t).Containers, ",") != "igdev-b" {
		t.Errorf("--rm was tallied as an orphan: %v", env.DockerOrphans(t).Containers)
	}

	// An unnamed container is tracked by the id the shim echoed, and removing it
	// by that id (or a prefix of it) clears it.
	echoed := env.RunShell(`docker create alpine`)
	id := strings.TrimSpace(echoed.Stdout)
	if len(id) != 64 {
		t.Fatalf("shim did not echo a container id, got %q", id)
	}
	if n := len(env.DockerOrphans(t).Containers); n != 2 {
		t.Errorf("containers after an unnamed create = %d, want 2 (igdev-b plus the new id)", n)
	}
	env.RunShell(`docker rm ` + id[:12])
	if strings.Join(env.DockerOrphans(t).Containers, ",") != "igdev-b" {
		t.Errorf("removing by id prefix did not clear the container: %v", env.DockerOrphans(t).Containers)
	}
	if unmatched := env.DockerOrphans(t).Unmatched; len(unmatched) != 1 {
		t.Errorf("prefix removal reported as unmatched: %v", unmatched)
	}
}
