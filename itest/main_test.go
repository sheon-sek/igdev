package itest

import (
	"os"
	"testing"

	"github.com/sheon-sek/igdev/internal/testrig"
)

// TestMain owns the two artifacts the suite builds once for the whole process:
// the binary under test (inside the rig) and the packaged release tree.
func TestMain(m *testing.M) {
	code := m.Run()
	CleanupDist()
	testrig.CleanupBinary()
	os.Exit(code)
}
