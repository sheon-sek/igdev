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

// AllocateFrom asks the kernel for a free loopback triplet whose HTTP port is
// httpPort: a machine-local pin (ADR 0003) fixes the port a person reads and
// pastes into a browser, and the other two are allocated as usual. A pinned port
// this host refuses is a named failure — quietly moving off a pin would make the
// pin a lie.
func AllocateFrom(httpPort int) (Triplet, *contract.Fault) {
	if httpPort < 1 || httpPort > 65535 {
		return Triplet{}, pinFault(httpPort, fmt.Errorf("it is not a port between 1 and 65535"))
	}
	listeners := make([]net.Listener, 0, 3)
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	pinned, err := net.Listen("tcp", net.JoinHostPort(BindAddress, strconv.Itoa(httpPort)))
	if err != nil {
		return Triplet{}, pinFault(httpPort, err)
	}
	listeners = append(listeners, pinned)
	picked := make([]int, 0, 2)
	for range 2 {
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
	return Triplet{HTTP: httpPort, HTTPS: picked[0], Debug: picked[1]}, nil
}

// pinFault is a pinned port this host will not give back: the pin has to change
// or the process holding it has to stop.
func pinFault(httpPort int, cause error) *contract.Fault {
	return contract.NewFault(contract.CodePortAlloc, contract.ExitFailure,
		fmt.Sprintf("the pinned Gateway port %d cannot be bound: %v", httpPort, cause)).
		WithCause(cause).
		WithRemediation(
			contract.Remediation{
				Command: "igdev setup",
				Why:     "allocate ports by bind probe instead of pinning one",
			},
			contract.Remediation{
				Command: "igdev doctor",
				Why:     "see which ports this machine already holds",
			})
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
