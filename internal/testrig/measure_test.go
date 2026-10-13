package testrig

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// lateAllocEnv makes the test binary the child of TestRSSPollKeepsLatePeak: it
// idles past the poller's first few hundred readings, then raises its high-water
// mark and holds it until the poller has read it.
const lateAllocEnv = "IGDEV_TESTRIG_LATE_ALLOC"

const lateAllocMiB = 64

func TestMain(m *testing.M) {
	if os.Getenv(lateAllocEnv) == "1" {
		time.Sleep(600 * time.Millisecond)
		block := make([]byte, lateAllocMiB<<20)
		for i := range block {
			block[i] = 1
		}
		time.Sleep(300 * time.Millisecond)
		os.Exit(int(block[len(block)-1]) - 1)
	}
	os.Exit(m.Run())
}

// A command whose peak comes after the poller's first 256 readings is measured at
// that peak: the poll keeps a running maximum, not the first readings it queued.
func TestRSSPollKeepsLatePeak(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("no /proc on this host")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lateAllocEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	collect := startRSSPoll(cmd.Process.Pid, time.Millisecond)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	peak := collect()
	if peak.samples <= 256 {
		t.Fatalf("the poll read %d samples, too few to cross the old 256-reading bound", peak.samples)
	}
	if want := int64(lateAllocMiB << 10); peak.peakKB < want {
		t.Errorf("peak = %d KiB after %d samples, want at least the late %d KiB allocation", peak.peakKB, peak.samples, want)
	}
}
