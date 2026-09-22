package testrig

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
)

// GatewayStub is a loopback HTTP server standing in for a running Ignition
// Gateway. The `gateway wait` and `gateway smoke` verbs probe it over the ports
// the Checkout Setup recorded, so the whole health path runs hermetically
// without an engine: the test decides which paths answer, and with what.
type GatewayStub struct {
	// URL is the stand-in's base URL, without a trailing slash.
	URL string
	// Port is the loopback port it listens on.
	Port int

	listener net.Listener
	server   *http.Server

	mu    sync.Mutex
	paths map[string]int
	hits  int
}

// ServeGateway starts a stand-in Gateway on a loopback port the fixture already
// owns (normally the recorded HTTP port). A request for a listed path is answered
// with that status; every other path is 404, which is the "not deployed yet"
// answer a real Gateway gives too. The server shuts down with the test.
func ServeGateway(t *testing.T, port int, paths map[string]int) *GatewayStub {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("bind the stand-in Gateway on port %d: %v", port, err)
	}
	stub := &GatewayStub{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", port),
		Port:     port,
		listener: listener,
		paths:    map[string]int{},
	}
	for path, status := range paths {
		stub.paths[path] = status
	}
	stub.server = &http.Server{Handler: http.HandlerFunc(stub.handle)}
	go func() { _ = stub.server.Serve(listener) }()
	t.Cleanup(stub.Close)
	return stub
}

// SetStatus changes one path's answer, so a test can make a healthy Gateway start
// failing a check.
func (g *GatewayStub) SetStatus(path string, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paths[path] = status
}

// Hits reports how many requests the stand-in answered.
func (g *GatewayStub) Hits() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hits
}

// Close shuts the stand-in down. It is idempotent, so t.Cleanup may run after an
// explicit call.
func (g *GatewayStub) Close() {
	if g.server != nil {
		_ = g.server.Close()
	}
}

func (g *GatewayStub) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	status, ok := g.paths[r.URL.Path]
	g.hits++
	g.mu.Unlock()
	if !ok {
		status = http.StatusNotFound
	}
	w.WriteHeader(status)
}
