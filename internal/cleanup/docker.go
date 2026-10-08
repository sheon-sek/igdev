package cleanup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/instance"
	"github.com/sheon-sek/igdev/internal/project"
)

// Runner runs one docker CLI invocation and returns its output.
type Runner func(args ...string) (stdout, stderr string, err error)

// Exec is the Runner that runs the real docker CLI.
func Exec(args ...string) (string, string, error) {
	cmd := exec.Command("docker", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// The labels that tie a Docker object to an igdev Instance. Compose sets the
// project and working_dir labels on everything it creates; igdev's own Compose
// file adds the instance label to its containers.
const (
	labelProject    = "com.docker.compose.project"
	labelWorkingDir = "com.docker.compose.project.working_dir"
	// LabelInstance carries the Instance UUID on every container igdev's Compose
	// file defines.
	LabelInstance = "dev.igdev.instance"
)

// BaseImageRepository is the official image every Instance image is built FROM.
const BaseImageRepository = "inductiveautomation/ignition"

// namespacePattern is an Instance namespace exactly: igdev- and eight hex
// digits. A user's own compose project named igdev-something never matches.
var namespacePattern = regexp.MustCompile(`^` + regexp.QuoteMeta(instance.NamespacePrefix) + `[0-9a-f]{` +
	fmt.Sprint(instance.ShortLength) + `}$`)

// IsNamespace reports whether name is an igdev Instance namespace.
func IsNamespace(name string) bool { return namespacePattern.MatchString(name) }

type container struct {
	name, project, workingDir, instance string
}

type labelled struct {
	name, project string
}

// engine is one read of the Docker objects that can belong to an Instance.
type engine struct {
	available  bool
	containers []container
	volumes    []labelled
	networks   []labelled
	images     []string // repository:tag
}

const tab = `{{"\t"}}`

// snapshot lists containers, volumes, networks and images in four calls. No
// docker CLI at all means there is nothing Docker-side to clean; an engine that
// does not answer is a fault, so the plan never silently omits its objects.
func snapshot(run Runner) (engine, *contract.Fault) {
	var e engine
	list := func(args ...string) ([]string, *contract.Fault, bool) {
		stdout, stderr, err := run(args...)
		if err != nil {
			if missing(err) {
				return nil, nil, false
			}
			return nil, docker.Fault("docker "+strings.Join(args[:2], " "), stdout+stderr, err), false
		}
		var rows []string
		for _, line := range strings.Split(stdout, "\n") {
			if strings.TrimSpace(line) != "" {
				rows = append(rows, strings.TrimRight(line, "\r"))
			}
		}
		return rows, nil, true
	}

	rows, fault, ok := list("ps", "--all", "--no-trunc", "--format",
		"{{.Names}}"+tab+`{{.Label "`+labelProject+`"}}`+tab+`{{.Label "`+labelWorkingDir+`"}}`+tab+`{{.Label "`+LabelInstance+`"}}`)
	if fault != nil || !ok {
		return e, fault
	}
	e.available = true
	for _, row := range rows {
		f := fields(row, 4)
		e.containers = append(e.containers, container{name: f[0], project: f[1], workingDir: checkoutOf(f[2]), instance: f[3]})
	}
	rows, fault, _ = list("volume", "ls", "--format", "{{.Name}}"+tab+`{{.Label "`+labelProject+`"}}`)
	if fault != nil {
		return e, fault
	}
	for _, row := range rows {
		f := fields(row, 2)
		e.volumes = append(e.volumes, labelled{name: f[0], project: f[1]})
	}
	rows, fault, _ = list("network", "ls", "--format", "{{.Name}}"+tab+`{{.Label "`+labelProject+`"}}`)
	if fault != nil {
		return e, fault
	}
	for _, row := range rows {
		f := fields(row, 2)
		e.networks = append(e.networks, labelled{name: f[0], project: f[1]})
	}
	rows, fault, _ = list("image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	if fault != nil {
		return e, fault
	}
	for _, row := range rows {
		if !strings.HasSuffix(row, ":<none>") {
			e.images = append(e.images, strings.TrimSpace(row))
		}
	}
	return e, nil
}

// fields splits a tab-separated row into exactly n columns.
func fields(row string, n int) []string {
	out := strings.SplitN(row, "\t", n)
	for len(out) < n {
		out = append(out, "")
	}
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

// owner is the namespace a volume or network belongs to: its compose project
// label, else the part of its name before compose's "_" separator.
func owner(object labelled) string {
	if object.project != "" {
		return object.project
	}
	name, _, _ := strings.Cut(object.name, "_")
	return name
}

// imageNamespace is the namespace an image reference belongs to, when its
// repository is an Instance namespace.
func imageNamespace(ref string) (string, string, bool) {
	repo, tag, _ := strings.Cut(ref, ":")
	if !IsNamespace(repo) {
		return "", "", false
	}
	return repo, tag, true
}

// namespaces are every igdev Instance the engine holds anything of.
func (e engine) namespaces() []string {
	set := map[string]bool{}
	for _, c := range e.containers {
		if IsNamespace(c.project) || (c.instance != "" && c.project != "") {
			set[c.project] = true
		}
	}
	for _, object := range append(append([]labelled{}, e.volumes...), e.networks...) {
		if ns := owner(object); IsNamespace(ns) {
			set[ns] = true
		}
	}
	for _, ref := range e.images {
		if ns, _, ok := imageNamespace(ref); ok {
			set[ns] = true
		}
	}
	return sortedKeys(set)
}

// projectsAt are the Instances whose containers compose created from root: a
// checkout whose `.igdev/` is already gone still finds its Gateway.
func (e engine) projectsAt(root string) []string {
	set := map[string]bool{}
	for _, c := range e.containers {
		if c.workingDir != "" && samePath(c.workingDir, root) && (IsNamespace(c.project) || c.instance != "") {
			set[c.project] = true
		}
	}
	return sortedKeys(set)
}

// workingDirs are the checkout directories namespace's containers were created
// from.
func (e engine) workingDirs(ns string) []string {
	set := map[string]bool{}
	for _, c := range e.containers {
		if c.project == ns && c.workingDir != "" {
			set[c.workingDir] = true
		}
	}
	return sortedKeys(set)
}

// checkoutOf maps compose's working_dir label onto the checkout. Compose records
// the directory of the first Compose file, and igdev's lives in
// .igdev/runtime/, so the checkout is two levels up from it.
func checkoutOf(workingDir string) string {
	if workingDir == "" {
		return ""
	}
	clean := filepath.Clean(workingDir)
	suffix := string(filepath.Separator) + filepath.Join(project.StateDir, "runtime")
	if strings.HasSuffix(clean, suffix) {
		return strings.TrimSuffix(clean, suffix)
	}
	return clean
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// instanceItems are the containers, networks, volumes and images of each
// namespace, in the order they can be removed: containers before what they use.
func (e engine) instanceItems(namespaces []string, run Runner) []*Item {
	var containers, networks, volumes, images []*Item
	for _, ns := range namespaces {
		for _, c := range e.containers {
			if c.project == ns {
				name := c.name
				containers = append(containers, &Item{Kind: KindContainer, Ref: name, Action: WouldRemove,
					remove: func() error { return dockerErr(run("rm", "--force", "--volumes", name)) }})
			}
		}
		for _, n := range e.networks {
			if owner(n) == ns {
				name := n.name
				networks = append(networks, &Item{Kind: KindNetwork, Ref: name, Action: WouldRemove,
					remove: func() error { return removeNetwork(run, name) }})
			}
		}
		for _, v := range e.volumes {
			if owner(v) == ns {
				name := v.name
				volumes = append(volumes, &Item{Kind: KindVolume, Ref: name, Action: WouldRemove,
					remove: func() error { return dockerErr(run("volume", "rm", "--force", name)) }})
			}
		}
		for _, ref := range e.images {
			if owner, _, ok := imageNamespace(ref); ok && owner == ns {
				ref := ref
				images = append(images, &Item{Kind: KindImage, Ref: ref, Action: WouldRemove,
					remove: func() error { return dockerErr(run("image", "rm", "--force", ref)) }})
			}
		}
	}
	out := append(containers, networks...)
	out = append(out, volumes...)
	return append(out, images...)
}

// baseImageItems are the official Ignition images on the engine. igdev pulls one
// for every version an Instance is built from, and an image igdev built is gone by
// the time a machine cleanup runs after a checkout cleanup, so its tag can no
// longer say which version it used: every tag is planned, the confirmation
// --machine needs lists each one, and the engine itself refuses to remove one a
// container still uses, which is then kept.
func (e engine) baseImageItems(run Runner) []*Item {
	var items []*Item
	for _, ref := range e.images {
		repo, _, _ := strings.Cut(ref, ":")
		if repo != BaseImageRepository {
			continue
		}
		ref := ref
		items = append(items, &Item{Kind: KindBaseImage, Ref: ref, Action: WouldRemove,
			// No --force: an image another container uses must stay.
			remove: func() error { return dockerErr(run("image", "rm", ref)) },
			keepOn: func(err error) (string, bool) {
				if strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "is being used") {
					return "still used by a container: " + err.Error(), true
				}
				return "", false
			}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })
	return items
}

// removeNetwork disconnects whatever is still attached — an act job container
// `ci-local --with-gateway` joined to the Instance's network — then removes it.
func removeNetwork(run Runner, name string) error {
	stdout, _, err := run("network", "inspect", "--format", `{{range .Containers}}{{.Name}}{{"\n"}}{{end}}`, name)
	if err == nil {
		for _, attached := range strings.Split(stdout, "\n") {
			if attached = strings.TrimSpace(attached); attached != "" {
				_, _, _ = run("network", "disconnect", "--force", name, attached)
			}
		}
	}
	return dockerErr(run("network", "rm", name))
}

// dockerErr turns a failed call into an error carrying the engine's own words. A
// "no such" answer means the object is already gone, which is the goal.
func dockerErr(stdout, stderr string, err error) error {
	if err == nil {
		return nil
	}
	output := strings.TrimSpace(stdout + stderr)
	if strings.Contains(strings.ToLower(output), "no such") {
		return nil
	}
	if output == "" {
		return err
	}
	lines := strings.Split(output, "\n")
	return errors.New(strings.TrimSpace(lines[len(lines)-1]))
}

func missing(err error) bool {
	var execErr *exec.Error
	return errors.As(err, &execErr) && errors.Is(execErr.Err, fs.ErrNotExist)
}

// recordedNamespace is the namespace dir's Checkout Setup records, or "".
func recordedNamespace(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, project.StateDir, project.SetupRecord))
	if err != nil {
		return ""
	}
	stamp, ok := gate.Decode(raw)
	if !ok {
		return ""
	}
	return stamp.Namespace()
}
