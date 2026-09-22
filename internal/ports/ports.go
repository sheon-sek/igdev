// Package ports allocates the three loopback ports an Instance publishes.
//
// Allocation is a bind probe, never a formula: igdev asks the kernel for a free
// loopback port three times, holding each listener open until all three are
// chosen so the triplet is distinct, then releases them and records the result
// in the Checkout Setup. Nothing in igdev ever assumes 8088 (ADR 0003).
package ports

import (
	"fmt"
	"net"
	"strconv"

	"github.com/sheon-sek/igdev/internal/contract"
)

// BindAddress is the interface every Instance publishes on. A development
// Gateway is never exposed beyond the machine.
const BindAddress = "127.0.0.1"

// Triplet is the three loopback ports one Instance publishes: the Gateway HTTP
// listener, its HTTPS listener, and the JVM debug port.
type Triplet struct {
	HTTP  int `json:"http"`
	HTTPS int `json:"https"`
	Debug int `json:"debug"`
}

// All returns the triplet's ports in the frozen order they are allocated,
// rendered, and reported.
func (t Triplet) All() []int { return []int{t.HTTP, t.HTTPS, t.Debug} }

// Complete reports whether all three ports were recorded. An incomplete triplet
// is not usable and is never reused.
func (t Triplet) Complete() bool {
	return t.HTTP > 0 && t.HTTPS > 0 && t.Debug > 0
}

// Allocate asks the kernel for three distinct free loopback ports.
func Allocate() (Triplet, *contract.Fault) {
	listeners := make([]net.Listener, 0, 3)
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	picked := make([]int, 0, 3)
	for range 3 {
		l, err := net.Listen("tcp", BindAddress+":0")
		if err != nil {
			return Triplet{}, allocFault(err)
		}
		listeners = append(listeners, l)
		addr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			return Triplet{}, allocFault(fmt.Errorf("listener reported a non-TCP address %s", l.Addr()))
		}
		picked = append(picked, addr.Port)
	}
	return Triplet{HTTP: picked[0], HTTPS: picked[1], Debug: picked[2]}, nil
}

// Free reports whether every port in the triplet can still be bound on loopback.
// It is what lets a re-setup keep a recorded triplet instead of allocating a new
// one, and what makes a port another process took in the meantime visible.
func (t Triplet) Free() bool {
	if !t.Complete() {
		return false
	}
	for _, port := range t.All() {
		l, err := net.Listen("tcp", net.JoinHostPort(BindAddress, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

func allocFault(err error) *contract.Fault {
	return contract.NewFault(contract.CodePortAlloc, contract.ExitFailure,
		fmt.Sprintf("cannot allocate a free loopback port: %v", err)).
		WithCause(err).
		WithRemediation(
			contract.Remediation{
				Command: "igdev doctor",
				Why:     "audit the host prerequisites igdev depends on",
			},
			contract.Remediation{
				Command: "igdev setup",
				Why:     "retry the allocation",
			})
}
