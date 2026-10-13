package cleanup

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/project"
)

// psRow is one `docker ps` row the inventory reads: name, state, project,
// working_dir, instance and service.
func psRow(name, state, ns, checkout, id, service string) string {
	wd := ""
	if checkout != "" {
		wd = filepath.Join(checkout, project.StateDir, "runtime")
	}
	return strings.Join([]string{name, state, ns, wd, id, service}, "\t")
}

// inventoryOf runs TakeInventory against canned ps rows and records every call.
func inventoryOf(t *testing.T, rows ...string) (Inventory, [][]string) {
	t.Helper()
	var calls [][]string
	inv := TakeInventory(func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return strings.Join(rows, "\n") + "\n", "", nil
	})
	return inv, calls
}

// setUp writes a Checkout Setup recording id under dir.
func setUp(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, project.StateDir), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema":1,"instance_id":"` + id + `"}`)
	if err := os.WriteFile(filepath.Join(dir, project.StateDir, project.SetupRecord), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func byNamespace(inv Inventory) map[string]InventoryInstance {
	out := map[string]InventoryInstance{}
	for _, in := range inv.Instances {
		out[in.Namespace] = in
	}
	return out
}

// Two worktrees of one repository are two Instances, each active in its own
// checkout; a deleted worktree's Instance is checkout_missing with a suggestion.
func TestInventoryClassifiesWorktrees(t *testing.T) {
	root := t.TempDir()
	one, two, gone := filepath.Join(root, "repo"), filepath.Join(root, "repo-wt"), filepath.Join(root, "deleted-wt")
	setUp(t, one, idA)
	setUp(t, two, idB)
	inv, calls := inventoryOf(t,
		psRow(nsA+"-gateway-1", "running", nsA, one, idA, "gateway"),
		psRow(nsA+"-trial-keeper-1", "running", nsA, one, idA, "trial-keeper"),
		psRow(nsB+"-gateway-1", "restarting", nsB, two, idB, "gateway"),
		psRow(nsC+"-gateway-1", "exited", nsC, gone, "cccccccc-0000-4000-8000-000000000000", "gateway"),
	)
	if inv.Engine != EngineAvailable || len(inv.Instances) != 3 {
		t.Fatalf("inventory = %+v, want 3 Instances on an available engine", inv)
	}
	got := byNamespace(inv)
	if a := got[nsA]; a.Classification != ClassActive || a.PathState != PathPresent || a.Checkout != one || a.Suggestion != "" || len(a.Services) != 2 {
		t.Errorf("first worktree = %+v, want active with two services and no suggestion", a)
	}
	if b := got[nsB]; b.Classification != ClassActive || b.Services[0].Restart != RestartRestartable {
		t.Errorf("second worktree = %+v, want active and restartable", b)
	}
	c := got[nsC]
	if c.Classification != ClassCheckoutMissing || c.PathState != PathMissing {
		t.Errorf("deleted worktree = %+v, want checkout_missing", c)
	}
	if c.Suggestion != "docker compose -p "+nsC+" stop" {
		t.Errorf("suggestion = %q, want a stop scoped to the one namespace", c.Suggestion)
	}
	if c.Services[0].Restart != RestartUnknown {
		t.Errorf("an exited container's restart group = %q, want unknown", c.Services[0].Restart)
	}
	assertReadOnly(t, calls)
}

// A moved checkout leaves a stale label: the old path is reported missing even
// though the Instance may be alive elsewhere, and a path that now holds another
// checkout's Setup needs review instead of counting as active.
func TestInventoryMovedCheckout(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	reused := filepath.Join(root, "reused")
	setUp(t, reused, idB) // another Instance now lives where nsA was created
	bare := filepath.Join(root, "no-setup")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	inv, _ := inventoryOf(t,
		psRow(nsA+"-gateway-1", "running", nsA, old, idA, "gateway"),
		psRow(nsB+"-gateway-1", "running", nsB, reused, idA, "gateway"),
		psRow(nsC+"-gateway-1", "running", nsC, bare, "", "gateway"),
	)
	got := byNamespace(inv)
	if got[nsA].Classification != ClassCheckoutMissing {
		t.Errorf("stale label = %+v, want checkout_missing (an observation, not deletion)", got[nsA])
	}
	// reused records idB, whose namespace is nsB: active.
	if got[nsB].Classification != ClassActive {
		t.Errorf("reused = %+v, want active", got[nsB])
	}
	if got[nsC].Classification != ClassNeedsReview || got[nsC].Suggestion == "" {
		t.Errorf("checkout without its Setup = %+v, want needs_review with a suggestion", got[nsC])
	}
}

// Another Instance's Setup at the recorded path needs review.
func TestInventoryPathNamesAnotherInstance(t *testing.T) {
	dir := t.TempDir()
	setUp(t, dir, idB)
	inv, _ := inventoryOf(t, psRow(nsA+"-gateway-1", "running", nsA, dir, idA, "gateway"))
	if in := inv.Instances[0]; in.Classification != ClassNeedsReview || in.PathState != PathPresent {
		t.Errorf("instance = %+v, want needs_review", in)
	}
}

// A user's own igdev-foo Compose project and any non-igdev project are never
// inventoried.
func TestInventoryIgnoresForeignProjects(t *testing.T) {
	inv, _ := inventoryOf(t,
		psRow("igdev-foo-web-1", "running", "igdev-foo", "/tmp/foo", "", "web"),
		psRow("hindsight-1", "running", "hindsight", "/srv/hindsight", "", "api"),
		psRow("loose", "running", "", "", "", ""),
	)
	if inv.Engine != EngineAvailable || inv.Instances == nil || len(inv.Instances) != 0 {
		t.Errorf("inventory = %+v, want an empty, non-nil list", inv)
	}
}

// An engine that cannot be asked is unknown, never zero Instances.
func TestInventoryEngineUnknown(t *testing.T) {
	cases := map[string]error{
		"daemon down": errors.New("exit status 1"),
		"no CLI":      &exec.Error{Name: "docker", Err: fs.ErrNotExist},
	}
	for name, err := range cases {
		inv := TakeInventory(func(...string) (string, string, error) {
			return "", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", err
		})
		if inv.Engine != EngineUnknown || inv.Error == "" || inv.Instances != nil {
			t.Errorf("%s: inventory = %+v, want unknown with a reason and no list", name, inv)
		}
	}
}

// A path that cannot be checked is unknown, never missing.
func TestInventoryPathUnknown(t *testing.T) {
	stat = func(string) (os.FileInfo, error) { return nil, fs.ErrPermission }
	t.Cleanup(func() { stat = os.Stat })
	inv, _ := inventoryOf(t, psRow(nsA+"-gateway-1", "running", nsA, "/mnt/unmounted/repo", idA, "gateway"))
	if in := inv.Instances[0]; in.PathState != PathUnknown || in.Classification != ClassUnknown || in.Suggestion == "" {
		t.Errorf("instance = %+v, want unknown path and classification", in)
	}
	inv, _ = inventoryOf(t, psRow(nsA+"-gateway-1", "running", nsA, "", idA, "gateway"))
	if in := inv.Instances[0]; in.PathState != PathUnknown {
		t.Errorf("no recorded path = %+v, want unknown", in)
	}
}

// assertReadOnly fails when the inventory asked the engine for anything but a
// listing.
func assertReadOnly(t *testing.T, calls [][]string) {
	t.Helper()
	if len(calls) != 1 || calls[0][0] != "ps" {
		t.Errorf("calls = %v, want one docker ps", calls)
	}
}
