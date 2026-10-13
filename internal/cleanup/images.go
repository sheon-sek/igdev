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
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return Image{}, err
	}
	if len(rows) != 1 || rows[0].ID != id {
		return Image{}, fmt.Errorf("unexpected image identity for %s", id)
	}
	tags := rows[0].Tags
	sort.Strings(tags)
	return Image{ID: id, Tags: tags, Project: rows[0].Config.Labels[labelProject]}, nil
}

func imageProtection(run Runner, image Image) (string, error) {
	ns := image.Namespace()
	for _, tag := range image.Tags {
		owner, _, ok := imageNamespace(tag)
		if !ok || owner != ns {
			return "image carries a tag outside the Instance: " + tag, nil
		}
	}
	out, stderr, err := run("ps", "--all", "--filter", "ancestor="+image.ID, "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("checking image users: %s: %w", strings.TrimSpace(out+stderr), err)
	}
	if strings.TrimSpace(out) != "" {
		return "still used by a container: " + strings.TrimSpace(out), nil
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

func imageItem(image Image, run Runner) *Item {
	item := &Item{Kind: KindImage, Ref: image.ID, Action: WouldRemove, keepOn: imageConflict}
	reason, err := imageProtection(run, image)
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
		reason, err := imageProtection(run, current)
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
