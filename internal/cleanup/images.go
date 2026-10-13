package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
)

// Image is a Docker image identified independently of its mutable tags.
// Compose's project label survives a rebuild that makes the image dangling.
type Image struct {
	ID      string
	Tags    []string
	Project string
	// layers are the image's RootFS layer digests, base first: an image built
	// FROM another starts with all of its layers.
	layers []string
}

// Namespace attributes an image to one Instance. A non-igdev Compose label
// takes precedence over tags; unlabelled images require an Instance tag.
func (i Image) Namespace() string {
	if i.Project != "" {
		if IsNamespace(i.Project) {
			return i.Project
		}
		return ""
	}
	for _, tag := range i.Tags {
		if ns, _, ok := imageNamespace(tag); ok {
			return ns
		}
	}
	return ""
}

// InventoryImages returns each full image ID once, including dangling images.
// Inspection failures abort inventory rather than silently omitting resources.
func InventoryImages(run Runner) ([]Image, *contract.Fault) {
	out, stderr, err := run("image", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, docker.Fault("docker image ls", out+stderr, err)
	}
	ids := map[string]bool{}
	for _, id := range strings.Fields(out) {
		ids[id] = true
	}
	var images []Image
	for _, id := range sortedKeys(ids) {
		image, err := inspectImage(run, id)
		if noSuchImage(err) {
			continue // removed concurrently between ls and inspect
		}
		if err != nil {
			return nil, docker.Fault("docker image inspect", err.Error(), err)
		}
		images = append(images, image)
	}
	return images, nil
}

func inspectImage(run Runner, id string) (Image, error) {
	out, stderr, err := run("image", "inspect", id)
	if err != nil {
		return Image{}, fmt.Errorf("%s: %s: %w", id, strings.TrimSpace(out+stderr), err)
	}
	var rows []struct {
		ID     string   `json:"Id"`
		Tags   []string `json:"RepoTags"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		RootFS struct {
			Layers []string `json:"Layers"`
		} `json:"RootFS"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return Image{}, err
	}
	if len(rows) != 1 || rows[0].ID != id {
		return Image{}, fmt.Errorf("unexpected image identity for %s", id)
	}
	tags := rows[0].Tags
	sort.Strings(tags)
	return Image{ID: id, Tags: tags, Project: rows[0].Config.Labels[labelProject], layers: rows[0].RootFS.Layers}, nil
}

func noSuchImage(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such image")
}

// imageProtection reports why an image must be kept. Containers named in
// leaving are ones the same plan removes first, so they do not protect it.
func imageProtection(run Runner, image Image, leaving map[string]bool) (string, error) {
	ns := image.Namespace()
	for _, tag := range image.Tags {
		owner, _, ok := imageNamespace(tag)
		if !ok || owner != ns {
			return "image carries a tag outside the Instance: " + tag, nil
		}
	}
	// Removal is by immutable ID only; untagging first would act on mutable
	// references, and the engine refuses an unforced ID removal of several tags.
	if len(image.Tags) > 1 {
		return "image carries several tags: " + strings.Join(image.Tags, ", "), nil
	}
	out, stderr, err := run("ps", "--all", "--filter", "ancestor="+image.ID, "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("checking image users: %s: %w", strings.TrimSpace(out+stderr), err)
	}
	var users []string
	for _, name := range strings.Fields(out) {
		if !leaving[name] {
			users = append(users, name)
		}
	}
	if len(users) > 0 {
		return "still used by a container: " + strings.Join(users, ", "), nil
	}
	return "", nil
}

type protectedImage struct{ reason string }

func (e protectedImage) Error() string { return e.reason }

func imageConflict(err error) (string, bool) {
	var protected protectedImage
	if errors.As(err, &protected) {
		return protected.reason, true
	}
	if strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "is being used") {
		return "engine refused image removal: " + err.Error(), true
	}
	return "", false
}

// imageItem plans one image. leaving names the containers this plan removes
// before its images; the execute-time recheck is authoritative.
func imageItem(image Image, run Runner, leaving map[string]bool) *Item {
	item := &Item{Kind: KindImage, Ref: image.ID, Action: WouldRemove, keepOn: imageConflict}
	reason, err := imageProtection(run, image, leaving)
	if err != nil {
		item.Action, item.Reason = Kept, "cannot verify image references: "+err.Error()
		return item
	}
	if reason != "" {
		item.Action, item.Reason = Kept, reason
		return item
	}
	item.remove = func() error {
		// Reinspect before deletion: tags and container references may have changed
		// since planning. Never force removal, even after these checks.
		current, err := inspectImage(run, image.ID)
		if err != nil {
			return dockerErr("", err.Error(), err)
		}
		if current.Namespace() != image.Namespace() {
			return protectedImage{"image ownership changed since planning"}
		}
		reason, err := imageProtection(run, current, nil)
		if err != nil {
			return err
		}
		if reason != "" {
			return protectedImage{reason}
		}
		return dockerErr(run("image", "rm", image.ID))
	}
	return item
}
