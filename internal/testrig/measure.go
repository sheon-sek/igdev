package testrig

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sample is one reading of a child process's peak resident set.
type sample struct {
	peakKB int64
}

// startRSSPoll watches /proc/<pid>/status for the child's VmHWM (kernel-tracked
// high-water mark, so a single reading is already the peak) and returns a
// collector that stops the poller and gathers the readings.
func startRSSPoll(pid int) func() []sample {
	stop := make(chan struct{})
	done := make(chan struct{})
	readings := make(chan sample, 256)
	go func() {
		defer close(readings)
		defer close(done)
		path := fmt.Sprintf("/proc/%d/status", pid)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if s, ok := readVmHWM(path); ok {
					select {
					case readings <- sample{peakKB: s}:
					default:
					}
				}
			}
		}
	}()
	return func() []sample {
		close(stop)
		out := []sample{}
		for s := range readings {
			out = append(out, s)
		}
		<-done
		return out
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
