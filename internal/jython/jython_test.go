package jython

import (
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// The cache paths are the frozen layout: one directory per version under the XDG
// cache root, holding the artifact every process on the machine shares.
func TestSpecPathsAndURL(t *testing.T) {
	spec := Spec{Version: "2.7.4", BaseURL: DefaultBaseURL, CacheRoot: "/cache/igdev"}
	if got, want := spec.JarPath(), "/cache/igdev/jython/2.7.4/jython-standalone-2.7.4.jar"; got != want {
		t.Errorf("JarPath = %q, want %q", got, want)
	}
	if got, want := spec.URL(), DefaultBaseURL+"/2.7.4/jython-standalone-2.7.4.jar"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	trailing := spec
	trailing.BaseURL += "/"
	if got := trailing.URL(); got != spec.URL() {
		t.Errorf("URL with a trailing slash = %q, want %q", got, spec.URL())
	}
}

// The embedded table is what makes an unverified download impossible, so the pin
// has to be a real digest.
func TestEmbeddedPinsAreDigests(t *testing.T) {
	pin, ok := Pins["2.7.4"]
	if !ok {
		t.Fatal("the embedded catalog pins no 2.7.4 artifact")
	}
	if !isDigest(pin) {
		t.Errorf("the embedded 2.7.4 pin %q is not a sha256 digest", pin)
	}
}

func TestExpectedDigest(t *testing.T) {
	pin := Pins["2.7.4"]

	pinned, fault := Spec{Version: "2.7.4"}.expectedDigest()
	if fault != nil || pinned != pin {
		t.Errorf("expectedDigest = %q, %v; want the embedded pin", pinned, fault)
	}

	override, fault := Spec{Version: "2.7.4", Digest: strings.ToUpper(pin)}.expectedDigest()
	if fault != nil || override != pin {
		t.Errorf("expectedDigest = %q, %v; want the override folded to lower case", override, fault)
	}

	_, fault = Spec{Version: "2.7.4", Digest: "nope"}.expectedDigest()
	if fault == nil || fault.Code != contract.CodeConfigInvalid {
		t.Errorf("a malformed override = %v, want %s", fault, contract.CodeConfigInvalid)
	}

	_, fault = Spec{Version: "2.7.3"}.expectedDigest()
	if fault == nil || fault.Code != contract.CodeJythonVersionUnsupported {
		t.Errorf("an unpinned version = %v, want %s", fault, contract.CodeJythonVersionUnsupported)
	}

	// An override changes what a pinned version must hash to; it cannot make an
	// unpinned version supported.
	_, fault = Spec{Version: "2.7.3", Digest: pin}.expectedDigest()
	if fault == nil || fault.Code != contract.CodeJythonVersionUnsupported {
		t.Errorf("an unpinned version with an override = %v, want %s", fault, contract.CodeJythonVersionUnsupported)
	}
}

// The driver's `path:line: message` lines are the contract the Go side parses; a
// line that does not match is kept verbatim rather than dropped, so a JVM-level
// failure still reaches the caller.
func TestParseDiagnostics(t *testing.T) {
	got := parseDiagnostics("/repo/src/bad.py:3: unmatched ')'\n\nplain jvm noise\n/repo/a.py:12: invalid syntax\n")
	want := []Diagnostic{
		{File: "/repo/src/bad.py", Line: 3, Message: "unmatched ')'"},
		{Message: "plain jvm noise"},
		{File: "/repo/a.py", Line: 12, Message: "invalid syntax"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseDiagnostics = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("diagnostic %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if rendered := got[0].String(); rendered != "/repo/src/bad.py:3: unmatched ')'" {
		t.Errorf("Diagnostic.String = %q", rendered)
	}
}
