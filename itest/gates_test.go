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
		{"stat", "--json"},   // failure paths must clean up too
		{"status", "--nope"}, // and usage errors
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := env.RunIn(root, args...)
			res.AssertNoLeaks(t)
		})
	}
	env.AssertNoDockerOrphans(t)
	env.AssertNoDockerCalls(t)
}

// The orphan tally in the rig is the assertion every later ticket reuses, so it
// is itself pinned here: created-but-not-removed objects are counted, removed
// ones are not.
func TestGateDockerOrphanTally(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()

	if got := env.DockerOrphans(t); got.Total() != 0 {
		t.Fatalf("fresh environment reports orphans: %+v", got)
	}
	// Two containers created, one removed; one network created and removed; one
	// volume created and never removed.
	scripts := []string{
		`docker run --name igdev-a -d alpine`,
		`docker run --name igdev-b -d alpine`,
		`docker rm igdev-a`,
		`docker network create igdev-net`,
		`docker network rm igdev-net`,
		`docker volume create igdev-store`,
	}
	for _, script := range scripts {
		env.RunShell(script)
	}
	got := env.DockerOrphans(t)
	if got.Containers != 1 || got.Networks != 0 || got.Volumes != 1 {
		t.Errorf("orphan tally = %+v, want 1 container and 1 volume", got)
	}
	if calls := env.DockerCalls(t); len(calls) != len(scripts) {
		t.Errorf("shim recorded %d calls, want %d", len(calls), len(scripts))
	}
	// The recorded argv is what the CLI actually asked for.
	last := env.DockerCalls(t)[len(scripts)-1]
	joined := ""
	for _, a := range last.Argv {
		joined += a + " "
	}
	if joined != "volume create igdev-store " {
		t.Errorf("recorded argv = %v", last.Argv)
	}
}
