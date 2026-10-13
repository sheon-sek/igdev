package cleanup

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/docker"
)

// The inventory is the read-only audit of every igdev Instance the engine still
// holds containers for, matched against the checkout its containers were created
// from. It lists and stats; it never stops, removes or writes anything.

// Engine states of an inventory.
const (
	// EngineAvailable means the engine answered, so the list is complete.
	EngineAvailable = "available"
	// EngineUnknown means the engine could not be asked: no docker CLI, or a
	// daemon that did not answer. It is never reported as zero Instances.
	EngineUnknown = "unknown"
)

// Checkout path states.
const (
	PathPresent = "present"
	PathMissing = "missing"
	// PathUnknown is a path that could not be checked: permissions, an
	// unmounted volume, or no recorded path at all.
	PathUnknown = "unknown"
)

// Instance classifications. A missing checkout is an observation, not proof the
// Instance is abandoned: a moved checkout keeps its stale working_dir label until
// its next `up`.
const (
	ClassActive          = "active"
	ClassNeedsReview     = "needs_review"
	ClassCheckoutMissing = "checkout_missing"
	ClassUnknown         = "unknown"
)

// Restart groups of one container under the `unless-stopped` policy.
const (
	// RestartRunning is a running container.
	RestartRunning = "running"
	// RestartRestartable is a container the engine is restarting, which comes
	// back on the next daemon or host start.
	RestartRestartable = "restartable"
	// RestartUnknown is a stopped container. The engine does not expose whether
	// an operator stopped it (kept stopped across reboots) or it is waiting to
	// be restarted, so igdev does not guess.
	RestartUnknown = "unknown"
)

// InventoryService is one container of an inventoried Instance.
type InventoryService struct {
	Service   string `json:"service"`
	Container string `json:"container"`
	State     string `json:"state"`
	Restart   string `json:"restart"`
}

// InventoryInstance is one Instance the engine holds containers for.
type InventoryInstance struct {
	Namespace string `json:"namespace"`
	Instance  string `json:"instance"`
	// Checkout is the checkout its containers were created from, per their
	// working_dir label; empty when none was recorded.
	Checkout       string             `json:"checkout"`
	PathState      string             `json:"path_state"`
	Classification string             `json:"classification"`
	Services       []InventoryService `json:"services"`
	// Suggestion is a reviewed remediation for a non-active Instance, shown and
	// never run: it stops this namespace's containers only, works without the
	// Compose file, and keeps the data volume and the Consent record.
	Suggestion string `json:"suggestion,omitempty"`
}

// Inventory is the whole audit.
type Inventory struct {
	Engine string `json:"engine"`
	// Error says why the engine is unknown; empty when available.
	Error     string              `json:"error,omitempty"`
	Instances []InventoryInstance `json:"items"`
}

// stat is os.Stat, replaceable in tests.
var stat = os.Stat

// TakeInventory lists every igdev Instance's containers in one docker call and
// classifies each Instance against its recorded checkout.
func TakeInventory(run Runner) Inventory {
	stdout, stderr, err := run("ps", "--all", "--no-trunc", "--format",
		"{{.Names}}"+tab+"{{.State}}"+tab+`{{.Label "`+labelProject+`"}}`+tab+
			`{{.Label "`+labelWorkingDir+`"}}`+tab+`{{.Label "`+LabelInstance+`"}}`+tab+
			`{{.Label "com.docker.compose.service"}}`)
	if err != nil {
		inv := Inventory{Engine: EngineUnknown}
		switch {
		case missing(err), errors.Is(err, exec.ErrNotFound):
			inv.Error = "docker is not on PATH"
		default:
			inv.Error = docker.Fault("docker ps", stdout+stderr, err).Message
		}
		return inv
	}
	inv := Inventory{Engine: EngineAvailable, Instances: []InventoryInstance{}}
	byNS := map[string]*InventoryInstance{}
	dirs := map[string]map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := fields(strings.TrimRight(line, "\r"), 6)
		name, state, ns, wd, id, service := f[0], f[1], f[2], f[3], f[4], f[5]
		// Only an Instance namespace is igdev's: a user's own igdev-foo project
		// or any other Compose project is not inventoried.
		if !IsNamespace(ns) {
			continue
		}
		in := byNS[ns]
		if in == nil {
			in = &InventoryInstance{Namespace: ns}
			byNS[ns] = in
			dirs[ns] = map[string]bool{}
		}
		if in.Instance == "" {
			in.Instance = id
		}
		if wd != "" {
			dirs[ns][checkoutOf(wd)] = true
		}
		in.Services = append(in.Services, InventoryService{Service: service, Container: name, State: state, Restart: restartGroup(state)})
	}
	for ns, in := range byNS {
		sort.Slice(in.Services, func(i, j int) bool { return in.Services[i].Container < in.Services[j].Container })
		classify(in, sortedKeys(dirs[ns]))
		inv.Instances = append(inv.Instances, *in)
	}
	sort.Slice(inv.Instances, func(i, j int) bool { return inv.Instances[i].Namespace < inv.Instances[j].Namespace })
	return inv
}

// classify sets the checkout, its path state, the classification and, for a
// non-active Instance, the suggested remediation.
func classify(in *InventoryInstance, checkouts []string) {
	in.PathState, in.Classification = PathUnknown, ClassUnknown
	switch len(checkouts) {
	case 0:
	case 1:
		in.Checkout = checkouts[0]
		in.PathState = pathState(in.Checkout)
		switch in.PathState {
		case PathMissing:
			in.Classification = ClassCheckoutMissing
		case PathPresent:
			if recordedNamespace(in.Checkout) == in.Namespace {
				in.Classification = ClassActive
			} else {
				in.Classification = ClassNeedsReview
			}
		}
	default:
		// Containers created from more than one directory: nothing says which
		// one owns the Instance now.
		in.Checkout = checkouts[0]
		in.Classification = ClassNeedsReview
	}
	if in.Classification != ClassActive {
		in.Suggestion = "docker compose -p " + in.Namespace + " stop"
	}
}

func pathState(path string) string {
	_, err := stat(path)
	switch {
	case err == nil:
		return PathPresent
	case errors.Is(err, fs.ErrNotExist):
		return PathMissing
	default:
		return PathUnknown
	}
}

func restartGroup(state string) string {
	switch state {
	case "running":
		return RestartRunning
	case "restarting":
		return RestartRestartable
	default:
		return RestartUnknown
	}
}
