package testrig

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rssPeak is what a poll of a child's /proc saw: the largest VmHWM it read, in
// KiB, and how many reads succeeded.
type rssPeak struct {
	peakKB  int64
	samples int
}

// defaultRSSInterval is how often a sampled run reads /proc: fast enough to catch
// a command that exits within a few milliseconds.
const defaultRSSInterval = 100 * time.Microsecond

// startRSSPoll watches /proc/<pid>/status for the child's VmHWM (kernel-tracked
// high-water mark, so a single reading is already the peak) every interval and
// returns a collector that stops the poller and reports the peak. The poller keeps
// a running maximum rather than a queue of readings, so a command that peaks late
// in a run of minutes is still measured.
func startRSSPoll(pid int, interval time.Duration) func() rssPeak {
	if interval <= 0 {
		interval = defaultRSSInterval
	}
	stop := make(chan struct{})
	result := make(chan rssPeak, 1)
	go func() {
		var seen rssPeak
		defer func() { result <- seen }()
		path := fmt.Sprintf("/proc/%d/status", pid)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if kb, ok := readVmHWM(path); ok {
					seen.samples++
					seen.peakKB = max(seen.peakKB, kb)
				}
			}
		}
	}()
	return func() rssPeak {
		close(stop)
		return <-result
	}
}

func readVmHWM(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, ok := strings.CutPrefix(line, "VmHWM:")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb, true
	}
	return 0, false
}

// Percentile returns the p-th percentile of sorted durations (p in 0..100).
func Percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(p/100*float64(len(sorted)-1) + 0.5)
	return sorted[rank]
}
