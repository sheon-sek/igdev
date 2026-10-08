// Package cleanup finds and removes what igdev created, for `igdev cleanup`.
//
// A run is two steps. Plan inventories, for one scope, every object igdev made
// that still exists — Docker containers, volumes, networks and images, the
// Checkout Setup, tracked files `init` wrote, machine-level directories, the
// binary — plus the things igdev leaves alone on purpose, reported as kept so a
// person knows what remains. Execute then removes the plan's items in order:
// Docker objects first, files last, so a failure part-way still leaves the
// Instance ID on disk for a re-run to find the rest. Nothing is removed by Plan,
// which is what `--dry-run` reports.
package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// Action is what happened, or would happen, to one item.
type Action string

const (
	// WouldRemove is a dry run's verdict for an item a real run removes.
	WouldRemove Action = "would_remove"
	// Removed means the item is gone.
	Removed Action = "removed"
	// Kept means the item stays on purpose; Reason says why and how to remove it.
	Kept Action = "kept"
	// Failed means removing the item failed; Reason carries the error.
	Failed Action = "failed"
)

// The item kinds a plan reports.
const (
	KindContainer   = "container"
	KindVolume      = "volume"
	KindNetwork     = "network"
	KindImage       = "image"
	KindBaseImage   = "base_image"
	KindSetup       = "checkout_setup"
	KindContract    = "contract"
	KindAgentsBlock = "agents_block"
	KindSkill       = "skill"
	KindDir         = "dir"
	KindBinary      = "binary"
	KindProfile     = "profile_entry"
	KindUserContent = "user_content"
	KindExternal    = "external"
)

// Item is one thing a plan names.
type Item struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Action Action `json:"action"`
	// Reason explains a kept or failed item, or notes a consequence of removing
	// one (Consent, for instance).
	Reason string `json:"reason,omitempty"`
	// Diff is the unified diff of an in-place edit (AGENTS.md, a login profile).
	Diff string `json:"diff,omitempty"`

	remove func() error
	// keepOn turns a removal error into Kept with this explanation, for an
	// object the engine refuses to remove because something else still uses it.
	keepOn func(error) (string, bool)
}

// Options says what one run covers and where things live.
type Options struct {
	// Root is the checkout's Project Root, or "" when the working directory is
	// not in a checkout.
	Root string
	// Namespace is the Instance namespace the Checkout Setup records, or "".
	Namespace string
	// ContractTOML is the Project Contract's bytes, read for the user content
	// --deinit leaves in place.
	ContractTOML []byte

	Deinit    bool
	Machine   bool
	Uninstall bool

	// Home and Dirs locate the machine-level files.
	Home string
	Dirs xdg.Dir
	// Executable is the running binary's resolved path, for --uninstall.
	Executable string
	// Environ is the process environment, for the variables reported as kept.
	Environ map[string]string

	// Agents names the AGENTS.md managed block init writes.
	Agents AgentsBlock

	// Docker runs one docker CLI invocation.
	Docker Runner
}

// AgentsBlock locates the managed block `igdev init` writes into AGENTS.md.
type AgentsBlock struct {
	File  string
	Start string
	End   string
}

// Plan is an ordered inventory. A removable item starts as WouldRemove, which
// is the whole of a dry run's answer; Execute replaces it with the outcome.
type Plan struct {
	Items []*Item
}

// Build inventories everything the options cover. It removes nothing. A Docker
// engine that cannot be reached is a fault, because planning without it would
// drop the Instance's objects from the plan and a run would then delete the
// record that finds them.
func Build(opts Options) (Plan, *contract.Fault) {
	var plan Plan
	engine, fault := snapshot(opts.Docker)
	if fault != nil {
		return plan, fault
	}

	namespaces := map[string]bool{}
	if opts.Namespace != "" {
		namespaces[opts.Namespace] = true
	}
	if opts.Root != "" {
		for _, ns := range engine.projectsAt(opts.Root) {
			namespaces[ns] = true
		}
	}
	if opts.Machine {
		for _, ns := range engine.namespaces() {
			namespaces[ns] = true
		}
	}
	plan.Items = append(plan.Items, engine.instanceItems(sortedKeys(namespaces), opts.Docker)...)
	if opts.Machine {
		plan.Items = append(plan.Items, engine.baseImageItems(opts.Docker)...)
	}

	setups := map[string]bool{}
	if opts.Root != "" {
		setups[opts.Root] = true
	}
	if opts.Machine {
		for _, ns := range sortedKeys(namespaces) {
			for _, dir := range engine.workingDirs(ns) {
				if recordedNamespace(dir) == ns {
					setups[dir] = true
				}
			}
		}
	}
	for _, root := range sortedKeys(setups) {
		if item := setupItem(root); item != nil {
			plan.Items = append(plan.Items, item)
		}
	}

	if opts.Deinit && opts.Root != "" {
		plan.Items = append(plan.Items, deinitItems(opts)...)
	}
	if opts.Machine {
		plan.Items = append(plan.Items, machineItems(opts, engine.available)...)
	}
	if opts.Uninstall {
		plan.Items = append(plan.Items, uninstallItems(opts)...)
	}
	return plan, nil
}

// Execute removes the plan's items in order and records each outcome. It never
// stops early: every item is attempted, so one stuck object does not leave the
// rest behind.
func (p Plan) Execute() {
	for _, item := range p.Items {
		if item.remove == nil {
			continue
		}
		err := item.remove()
		switch {
		case err == nil:
			item.Action = Removed
		case item.keepOn != nil:
			if reason, ok := item.keepOn(err); ok {
				item.Action, item.Reason = Kept, reason
				continue
			}
			item.Action, item.Reason = Failed, err.Error()
		default:
			item.Action, item.Reason = Failed, err.Error()
		}
	}
}

// Count tallies the items with action a.
func (p Plan) Count(a Action) int {
	n := 0
	for _, item := range p.Items {
		if item.Action == a {
			n++
		}
	}
	return n
}

// Pending is how many items a run would remove.
func (p Plan) Pending() int {
	n := 0
	for _, item := range p.Items {
		if item.remove != nil {
			n++
		}
	}
	return n
}

// setupItem is a checkout's `.igdev/`, when it exists.
func setupItem(root string) *Item {
	dir := filepath.Join(root, project.StateDir)
	if _, err := os.Lstat(dir); err != nil {
		return nil
	}
	return &Item{Kind: KindSetup, Ref: dir, Action: WouldRemove, remove: func() error { return os.RemoveAll(dir) }}
}

// machineItems are igdev's XDG directories and global skills, plus the things on
// the machine igdev uses but does not own, reported as kept.
func machineItems(opts Options, docker bool) []*Item {
	var items []*Item
	for _, dir := range []struct {
		path, note string
	}{
		{opts.Dirs.Cache, ""},
		{opts.Dirs.State, ""},
		{opts.Dirs.Config, "holds the Consent record: a person accepts the Ignition EULA again before the next Gateway starts"},
	} {
		if dir.path == "" {
			continue
		}
		if _, err := os.Lstat(dir.path); err != nil {
			continue
		}
		path := dir.path
		items = append(items, &Item{Kind: KindDir, Ref: path, Action: WouldRemove, Reason: dir.note,
			remove: func() error { return os.RemoveAll(path) }})
	}
	items = append(items, skillItems(opts.Home, false)...)

	external := func(ref, reason string) {
		items = append(items, &Item{Kind: KindExternal, Ref: ref, Action: Kept, Reason: reason})
	}
	if docker {
		external("docker build cache", "shared with every image build on this machine; remove it with: docker builder prune")
	}
	if path := filepath.Join(opts.Home, ".cache", "act"); exists(path) {
		external(path, "act's own cache, with the runner images act pulled; remove them with act's or docker's own commands")
	}
	if path := filepath.Join(opts.Home, ".actrc"); exists(path) {
		external(path, "act's own configuration")
	}
	if path := opts.Environ["IGDEV_CONSENT_FILE"]; path != "" {
		external(path, "IGDEV_CONSENT_FILE names a file a person keeps; unset the variable and delete the file yourself")
	}
	if opts.Environ["IGDEV_ACCEPT_EULA"] != "" {
		external("IGDEV_ACCEPT_EULA", "set in this environment's configuration by a person; remove it there")
	}
	return items
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// removeIfEmpty removes dir when it holds nothing. A directory that still holds
// something, or is already gone, is left as it is.
func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// errorf wraps a removal failure with the path it concerns.
func errorf(path string, err error) error {
	return fmt.Errorf("%s: %w", path, err)
}

// blank reports whether raw holds nothing but white space.
func blank(raw []byte) bool { return strings.TrimSpace(string(raw)) == "" }
