package cleanup

import (
	"fmt"
	"sort"
	"strings"
)

// obsoleteItems plan `cleanup --obsolete-images`: the images a version change
// left behind, while the Instance itself, its data volume, the Checkout Setup and
// the Consent record stay. Two classes are planned, each under its own rule:
//
//   - this Instance's own images (attributed by Compose project label or
//     igdev-<id8> tag) not built for the current [ignition].version, with the
//     same protections a checkout cleanup applies;
//   - official inductiveautomation/ignition images. They carry no igdev label,
//     and nothing records which versions igdev pulled (ADR 0013), so one is
//     planned only when an obsolete image of this Instance was built from it and
//     no other image, container or the current contract still references it.
//     Every other official tag is listed as kept with the reason.
//
// Like every image item, an official image is removed by its immutable ID and
// never forced, and one that carries several tags is kept. The official images
// go first: an Instance image built from one whose removal failed is held back,
// so a retry can still attribute it.
func (e engine) obsoleteItems(ns, version string, run Runner) []*Item {
	current := ns + ":" + version
	var images []*Item
	removing := map[string]bool{}
	builtFrom := map[string][]*Item{} // official image ID -> Instance images built from it
	for _, image := range e.images {
		if image.Namespace() != ns {
			continue
		}
		if containsString(image.Tags, current) {
			images = append(images, &Item{Kind: KindImage, Ref: image.ID, Action: Kept,
				Reason: "built for the current [ignition].version " + version})
			continue
		}
		item := imageItem(image, run, nil)
		images = append(images, item)
		if item.Action != WouldRemove {
			continue
		}
		removing[image.ID] = true
		for _, base := range e.images {
			if isBaseImage(base) && (builtOn(image, base) || sameVersion(image, base)) {
				builtFrom[base.ID] = append(builtFrom[base.ID], item)
			}
		}
	}

	var bases []*Item
	for _, image := range e.images {
		if !isBaseImage(image) {
			continue
		}
		if len(image.Tags) > 1 {
			for _, ref := range image.Tags {
				if repo, _, _ := strings.Cut(ref, ":"); repo == BaseImageRepository {
					bases = append(bases, &Item{Kind: KindBaseImage, Ref: ref, Action: Kept,
						Reason: "image carries several tags: " + strings.Join(image.Tags, ", ")})
				}
			}
			continue
		}
		bases = append(bases, e.baseItem(image, version, builtFrom[image.ID], removing, run))
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i].Ref < bases[j].Ref })
	return append(bases, images...)
}

// baseItem plans one singly tagged official image under the reference rule
// obsoleteItems describes, and ties the Instance images built from it to its
// outcome.
func (e engine) baseItem(image Image, version string, attributed []*Item, removing map[string]bool, run Runner) *Item {
	ref := image.Tags[0]
	_, v, _ := strings.Cut(ref, ":")
	item := &Item{Kind: KindBaseImage, Ref: ref, Action: Kept}
	switch {
	case v == version:
		item.Reason = "the current [ignition].version"
		return item
	case len(attributed) == 0:
		item.Reason = "no obsolete image of this Instance was built from it, and igdev keeps what it cannot attribute; remove it yourself with: docker image rm " + ref
		return item
	}
	reason, err := baseReference(run, e.images, image, v, removing)
	if err != nil {
		item.Reason = "cannot verify image references: " + err.Error()
		return item
	}
	if reason != "" {
		item.Reason = reason
		return item
	}
	item.Action, item.keepOn = WouldRemove, imageConflict
	item.Reason = "removed by image ID " + image.ID
	item.remove = func() error {
		// Reread the engine: the tag may have moved, or an image or container may
		// have started using the image since planning. Remove by the planned ID
		// only, and never force.
		current, err := inspectImage(run, image.ID)
		if noSuchImage(err) {
			return nil
		}
		if err != nil {
			return dockerErr("", err.Error(), err)
		}
		if strings.Join(current.Tags, ",") != ref {
			return protectedImage{"image tags changed since planning: " + strings.Join(current.Tags, ", ")}
		}
		images, fault := InventoryImages(run)
		if fault != nil {
			return fault
		}
		reason, err := baseReference(run, images, current, v, removing)
		if err != nil {
			return err
		}
		if reason != "" {
			return protectedImage{reason}
		}
		return dockerErr(run("image", "rm", image.ID))
	}
	for _, image := range attributed {
		hold(image, item)
	}
	return item
}

// baseReference says why an official image must stay: another image is built on
// it or tagged for its version, or a container uses it. Images the plan removes
// do not count.
func baseReference(run Runner, images []Image, base Image, v string, removing map[string]bool) (string, error) {
	if len(base.layers) == 0 {
		return "its layers are unknown, so the images built on it cannot be found", nil
	}
	for _, other := range images {
		if other.ID == base.ID || removing[other.ID] {
			continue
		}
		for _, tag := range other.Tags {
			if _, tv, ok := imageNamespace(tag); ok && tv == v {
				return "still the base of " + tag, nil
			}
		}
		if builtOn(other, base) {
			return "still the base of " + displayName(other), nil
		}
	}
	out, stderr, err := run("ps", "--all", "--filter", "ancestor="+base.ID, "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("checking image users: %s: %w", strings.TrimSpace(out+stderr), err)
	}
	if users := strings.Fields(out); len(users) > 0 {
		return "still used by a container: " + strings.Join(users, ", "), nil
	}
	return "", nil
}

// hold keeps an Instance image whose official image failed to go: while it is on
// the engine, a retry can still attribute that image to this Instance.
func hold(image, base *Item) {
	remove := image.remove
	image.remove = func() error {
		if base.Action == Failed {
			return protectedImage{"kept so a retry can still attribute " + base.Ref + ", whose removal failed: run cleanup again"}
		}
		return remove()
	}
}

func isBaseImage(image Image) bool {
	for _, ref := range image.Tags {
		if repo, _, _ := strings.Cut(ref, ":"); repo == BaseImageRepository {
			return true
		}
	}
	return false
}

// sameVersion reports whether an Instance image is tagged for an official
// image's version: the Instance image is built FROM that tag.
func sameVersion(image, base Image) bool {
	for _, tag := range image.Tags {
		_, v, ok := imageNamespace(tag)
		if ok && containsString(base.Tags, BaseImageRepository+":"+v) {
			return true
		}
	}
	return false
}

// builtOn reports whether image was built FROM base: it starts with all of
// base's layers.
func builtOn(image, base Image) bool {
	if len(base.layers) == 0 || len(image.layers) < len(base.layers) {
		return false
	}
	for i := range base.layers {
		if image.layers[i] != base.layers[i] {
			return false
		}
	}
	return true
}

func displayName(image Image) string {
	if len(image.Tags) > 0 {
		return image.Tags[0]
	}
	return image.ID
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
