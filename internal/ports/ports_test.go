package ports

import (
	"net"
	"strconv"
	"testing"
)

// Allocation is by bind probe: the three ports come back distinct and bindable,
// which is the whole reason nothing has to assume 8088 (ADR 0003).
func TestAllocateReturnsThreeDistinctBindablePorts(t *testing.T) {
	triplet, fault := Allocate()
	if fault != nil {
		t.Fatalf("Allocate: %v", fault)
	}
	if !triplet.Complete() {
		t.Fatalf("Allocate returned an incomplete triplet: %+v", triplet)
	}
	seen := map[int]bool{}
	for _, port := range triplet.All() {
		if port <= 0 {
			t.Errorf("port %d is not a real port", port)
		}
		if seen[port] {
			t.Errorf("Allocate returned %d twice: %+v", port, triplet)
		}
		seen[port] = true
	}
	if !triplet.Free() {
		t.Errorf("a freshly allocated triplet reports itself as taken: %+v", triplet)
	}
}

// A recorded port another process holds is not free, which is what makes a
// re-setup re-allocate instead of recording a collision.
func TestFreeReportsAPortAnotherProcessHolds(t *testing.T) {
	triplet, fault := Allocate()
	if fault != nil {
		t.Fatalf("Allocate: %v", fault)
	}
	held, err := net.Listen("tcp", net.JoinHostPort(BindAddress, strconv.Itoa(triplet.Debug)))
	if err != nil {
		t.Fatalf("hold %d: %v", triplet.Debug, err)
	}
	defer held.Close()

	if triplet.Free() {
		t.Errorf("Free reported %+v free while %d is held", triplet, triplet.Debug)
	}
}

// An incomplete triplet is never reused, so a hand-edited or half-written record
// cannot make igdev keep a port it never allocated.
func TestIncompleteTripletIsNotFree(t *testing.T) {
	for _, triplet := range []Triplet{
		{},
		{HTTP: 18070},
		{HTTP: 18070, HTTPS: 19070},
	} {
		if triplet.Complete() {
			t.Errorf("%+v reports itself complete", triplet)
		}
		if triplet.Free() {
			t.Errorf("%+v reports itself free", triplet)
		}
	}
}

// The published interface is loopback only: a development Gateway is never
// reachable from the network.
func TestBindAddressIsLoopback(t *testing.T) {
	if BindAddress != "127.0.0.1" {
		t.Errorf("BindAddress = %q, want 127.0.0.1", BindAddress)
	}
}
