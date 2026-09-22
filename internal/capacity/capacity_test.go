package capacity

import (
	"os"
	"path/filepath"
	"testing"
)

// The rule has one boundary and it is exact: a machine with precisely the
// requested heap plus headroom free starts the Gateway, one MiB less does not.
func TestDecideBoundary(t *testing.T) {
	const requested = 2048
	required := requested + HeadroomMB
	for _, tc := range []struct {
		available int
		meets     bool
	}{
		{required + 1, true},
		{required, true},
		{required - 1, false},
		{0, false},
	} {
		got := Decide(tc.available, requested)
		if got.Meets() != tc.meets {
			t.Errorf("Decide(%d, %d).Meets() = %v, want %v", tc.available, requested, got.Meets(), tc.meets)
		}
		if got.RequiredMB() != required {
			t.Errorf("Decide(%d, %d).RequiredMB() = %d, want %d", tc.available, requested, got.RequiredMB(), required)
		}
		if !got.Measured {
			t.Errorf("Decide(%d, %d) reported an unmeasured host", tc.available, requested)
		}
	}
}

// An unmeasured host never refuses: there is no evidence of a shortage, and the
// gate cannot block work on a machine whose memory it cannot read.
func TestUnmeasuredHostAlwaysMeets(t *testing.T) {
	got := Unmeasured(65536)
	if got.Measured {
		t.Error("Unmeasured reported a measurement")
	}
	if !got.Meets() {
		t.Error("an unmeasured host refused to start a Gateway")
	}
}

func TestMeasureReadsMemAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	body := "MemTotal:       32768000 kB\nMemFree:         1000000 kB\nMemAvailable:   16308652 kB\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write meminfo fixture: %v", err)
	}
	got, err := Measure(path)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if want := 16308652 / 1024; got != want {
		t.Errorf("Measure = %d MiB, want %d", got, want)
	}
}

// A file without the field, and a file that is not there, are both "not
// measured": the caller turns either into an unmeasured Decision.
func TestMeasureFailures(t *testing.T) {
	dir := t.TempDir()
	noField := filepath.Join(dir, "no-field")
	if err := os.WriteFile(noField, []byte("MemTotal: 1024 kB\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, path := range []string{noField, filepath.Join(dir, "missing")} {
		if _, err := Measure(path); err == nil {
			t.Errorf("Measure(%s) succeeded, want an error", path)
		}
	}
}
