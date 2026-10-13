package itest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/testrig"
)

// The lifecycle verbs the resource-lifecycle fixes changed (igdev#140), repeated
// against the docker shim: every cycle leaves no docker object and no temp file
// behind, so the hygiene gate covers more than status. This is shim evidence for
// igdev's own bookkeeping only; what a real engine does with the same calls is the
// opt-in soak's job (soak_test.go), and docs/lifecycle-matrix.md says which
// claim needs which.
func TestLifecycleCyclesLeaveNoOrphansOrLeaks(t *testing.T) {
	env := testrig.NewEnv(t)
	env.ShimDocker()
	env.ShimMeminfo(65536)
	dir, _ := gatewayFixture(t, env, testrig.MinimalContract)

	for cycle := 1; cycle <= 3; cycle++ {
		if cycle > 1 {
			testrig.WantExit(t, env.RunIn(dir, "setup"), contract.ExitOK)
		}
		for _, args := range [][]string{
			{"gateway", "up", "--json"},
			{"gateway", "status", "--json"},
			{"gateway", "logs", "--tail", "50"},
			{"doctor", "--json"},
			{"cleanup", "--obsolete-images", "--dry-run", "--json"},
			{"gateway", "down", "--json"},
			{"gateway", "up", "--json"},
			{"cleanup", "--json"},
		} {
			step := fmt.Sprintf("cycle %d: %s", cycle, strings.Join(args, " "))
			res := env.RunIn(dir, args...)
			if res.Exit != int(contract.ExitOK) {
				t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", step, res.Exit, res.Stdout, res.Stderr)
			}
			env.AssertTempDirEmpty(t)
		}
		if _, err := os.Stat(filepath.Join(dir, project.StateDir)); !os.IsNotExist(err) {
			t.Fatalf("cycle %d: .igdev/ survived cleanup: %v", cycle, err)
		}
		env.AssertNoDockerOrphans(t)
		env.AssertNoPartialWrites(t)
	}
}
