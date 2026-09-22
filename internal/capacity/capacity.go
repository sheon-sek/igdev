// Package capacity implements the Capacity Gate (ADR 0003): the machine-level
// precondition a Gateway must pass before one more container is started.
//
// The rule is deliberately blunt and host-wide: starting a Gateway is refused
// when the memory the kernel says is available (MemAvailable) is below the heap
// the Project Contract requests plus fixed headroom for the rest of the machine.
// An OOM kill takes down whatever the kernel picks, so the guard is on the
// machine, not on the Instance.
//
// Measurement is a small interface: a path to a meminfo-format file. Linux
// reports /proc/meminfo; the test rig points MeminfoEnv at a fixture, which is
// the only seam that makes the refusal reachable from a hermetic test.
package capacity

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// HeadroomMB is the free memory a Gateway leaves for everything else: the
	// JVM is not the only process on the machine, and a Gateway that fits exactly
	// still swaps.
	HeadroomMB = 512

	// MeminfoPath is the kernel file that reports MemAvailable on Linux. It is
	// the only place the number is available without root.
	MeminfoPath = "/proc/meminfo"

	// MeminfoEnv names the environment override for MeminfoPath. It is the test
	// rig's seam, not a user setting: nothing in the CLI Contract is configured
	// here.
	MeminfoEnv = "IGDEV_SHIM_MEMINFO"
)

// Decision is the Capacity Gate's verdict for one requested heap.
type Decision struct {
	// RequestedMB is the heap the Project Contract asked the Gateway for.
	RequestedMB int `json:"requested_mb"`
	// HeadroomMB is the fixed slack the gate keeps free.
	HeadroomMB int `json:"headroom_mb"`
	// AvailableMB is the measured MemAvailable, zero when unmeasured.
	AvailableMB int `json:"available_mb"`
	// Measured reports whether the host offered a number at all.
	Measured bool `json:"measured"`
}

// RequiredMB is the free memory starting this Gateway needs.
func (d Decision) RequiredMB() int { return d.RequestedMB + d.HeadroomMB }

// Meets reports whether the machine has room. An unmeasured host always meets the
// gate: there is no evidence of a shortage, and refusing to start anything on a
// host whose memory cannot be read would make the tool unusable there.
func (d Decision) Meets() bool { return !d.Measured || d.AvailableMB >= d.RequiredMB() }

// Summary states the verdict in one human clause, so the refusal message can
// report the numbers instead of only the rule.
func (d Decision) Summary() string {
	if !d.Measured {
		return fmt.Sprintf("free memory could not be measured (needed %d MiB: %d MiB heap plus %d MiB headroom)",
			d.RequiredMB(), d.RequestedMB, d.HeadroomMB)
	}
	return fmt.Sprintf("%d MiB available, %d MiB required (%d MiB heap plus %d MiB headroom)",
		d.AvailableMB, d.RequiredMB(), d.RequestedMB, d.HeadroomMB)
}

// Decide applies the rule to a measurement. It is pure, which is what lets the
// boundary be tested exactly: available == required is a pass, one MiB less is
// not.
func Decide(availableMB, requestedMB int) Decision {
	return Decision{
		RequestedMB: requestedMB,
		HeadroomMB:  HeadroomMB,
		AvailableMB: availableMB,
		Measured:    true,
	}
}

// Unmeasured is the decision for a host whose free memory could not be read.
func Unmeasured(requestedMB int) Decision {
	return Decision{RequestedMB: requestedMB, HeadroomMB: HeadroomMB}
}

// Path is the meminfo file this process reads: the rig's override when it is set,
// the kernel's own file otherwise.
func Path() string {
	if path := os.Getenv(MeminfoEnv); path != "" {
		return path
	}
	return MeminfoPath
}

// Check measures the host and decides. A measurement failure returns an
// unmeasured Decision together with the reason, so the caller can say why the
// gate is not in force.
func Check(requestedMB int) (Decision, error) {
	available, err := Measure(Path())
	if err != nil {
		return Unmeasured(requestedMB), err
	}
	return Decide(available, requestedMB), nil
}

// Measure reads MemAvailable out of a meminfo-format file, in MiB.
func Measure(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			break
		}
		kib, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, fmt.Errorf("%s: MemAvailable is not a number: %q", path, fields[0])
		}
		return kib / 1024, nil
	}
	return 0, fmt.Errorf("%s carries no MemAvailable line", path)
}
