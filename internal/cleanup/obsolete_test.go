package cleanup

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

const ignition = BaseImageRepository + ":"

// imageEngine answers the image calls --obsolete-images makes from in-memory
// images, and records every image removal.
type imageEngine struct {
	t       *testing.T
	images  map[string]*Image
	users   map[string]string // image ID -> container names using it
	rmFail  map[string]string // image ID -> engine error output
	removed []string
}

func (f *imageEngine) add(id, project string, layers []string, tags ...string) {
	f.images[id] = &Image{ID: id, Project: project, Tags: tags, layers: layers}
}

func (f *imageEngine) byRef(ref string) *Image {
	if image, ok := f.images[ref]; ok {
		return image
	}
	for _, image := range f.images {
		if containsString(image.Tags, ref) {
			return image
		}
	}
	return nil
}

func (f *imageEngine) run(args ...string) (string, string, error) {
	switch {
	case args[0] == "ps" && len(args) > 3 && args[2] == "--filter":
		return f.users[strings.TrimPrefix(args[3], "ancestor=")], "", nil
	case args[0] == "ps":
		return nsA + "-gateway-1\t" + nsA + "\t/repo/.igdev/runtime\t" + idA, "", nil
	case args[0] == "volume" && args[1] == "ls":
		return nsA + "_data\t" + nsA, "", nil
	case args[0] == "network" && args[1] == "ls":
		return nsA + "_default\t" + nsA, "", nil
	case args[0] == "image" && args[1] == "ls":
		var ids []string
		for id := range f.images {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return strings.Join(ids, "\n"), "", nil
	case args[0] == "image" && args[1] == "inspect":
		image := f.byRef(args[len(args)-1])
		if image == nil {
			return "", "Error response from daemon: No such image: " + args[len(args)-1], errors.New("exit status 1")
		}
		raw, _ := json.Marshal([]map[string]any{{"Id": image.ID, "RepoTags": image.Tags,
			"Config": map[string]any{"Labels": map[string]string{labelProject: image.Project}},
			"RootFS": map[string]any{"Layers": image.layers}}})
		return string(raw), "", nil
	case args[0] == "image" && args[1] == "rm":
		// Removal is by immutable ID only, never by tag and never forced.
		if len(args) != 3 || !strings.HasPrefix(args[2], "sha256:") {
			f.t.Fatalf("unsafe image removal: %v", args)
		}
		id := args[2]
		if out, ok := f.rmFail[id]; ok {
			return "", out, errors.New("exit status 1")
		}
		if f.images[id] == nil {
			return "", "Error: No such image: " + id, errors.New("exit status 1")
		}
		f.removed = append(f.removed, id)
		delete(f.images, id)
		return "", "", nil
	default:
		f.t.Fatalf("unexpected docker call: %v", args)
	}
	return "", "", nil
}

// An Instance moved from 8.3.7 to 8.3.8, after earlier visits to 8.3.6 and 8.3.5.
// Another checkout's Instance still runs 8.3.6, and a container uses 8.3.5's base
// directly. A person pulled 8.1.0 themselves.
func versionChange(t *testing.T) *imageEngine {
	f := &imageEngine{t: t, images: map[string]*Image{}, users: map[string]string{}, rmFail: map[string]string{}}
	f.add("sha256:b88", "", []string{"L88"}, ignition+"8.3.8")
	f.add("sha256:b87", "", []string{"L87"}, ignition+"8.3.7")
	f.add("sha256:b86", "", []string{"L86"}, ignition+"8.3.6")
	f.add("sha256:b85", "", []string{"L85"}, ignition+"8.3.5")
	f.add("sha256:b81", "", []string{"L81"}, ignition+"8.1.0")
	f.add("sha256:cur", nsA, []string{"L88", "a1"}, nsA+":8.3.8")
	f.add("sha256:old", nsA, []string{"L87", "a2"}, nsA+":8.3.7")
	f.add("sha256:o86", nsA, []string{"L86", "a3"}, nsA+":8.3.6")
	f.add("sha256:o85", nsA, []string{"L85", "a4"}, nsA+":8.3.5")
	f.add("sha256:dangling", nsA, []string{"L88", "a5"})
	f.add("sha256:b-86", nsB, []string{"L86", "b1"}, nsB+":8.3.6")
	f.users["sha256:cur"] = nsA + "-gateway-1"
	f.users["sha256:b85"] = "someones-own-container"
	return f
}

func obsoletePlan(t *testing.T, f *imageEngine) Plan {
	t.Helper()
	plan, fault := Build(Options{Root: "/repo", Namespace: nsA, ObsoleteImages: true, IgnitionVersion: "8.3.8", Docker: f.run})
	if fault != nil {
		t.Fatal(fault)
	}
	return plan
}

func itemFor(p Plan, kind, ref string) *Item {
	for _, item := range p.Items {
		if item.Kind == kind && item.Ref == ref {
			return item
		}
	}
	return nil
}

func TestObsoleteImagesPlanKeepsTheInstance(t *testing.T) {
	f := versionChange(t)
	plan := obsoletePlan(t, f)

	want := []struct {
		kind, ref string
		action    Action
		reason    string
	}{
		{KindBaseImage, ignition + "8.1.0", Kept, "cannot attribute"},
		{KindBaseImage, ignition + "8.3.5", Kept, "someones-own-container"},
		{KindBaseImage, ignition + "8.3.6", Kept, "base of " + nsB + ":8.3.6"},
		{KindBaseImage, ignition + "8.3.7", WouldRemove, ""},
		{KindBaseImage, ignition + "8.3.8", Kept, "current [ignition].version"},
		{KindImage, "sha256:cur", Kept, "current [ignition].version 8.3.8"},
		{KindImage, "sha256:dangling", WouldRemove, ""},
		{KindImage, "sha256:o85", WouldRemove, ""},
		{KindImage, "sha256:o86", WouldRemove, ""},
		{KindImage, "sha256:old", WouldRemove, ""},
	}
	if len(plan.Items) != len(want) {
		t.Fatalf("plan has %d items, want %d: %+v", len(plan.Items), len(want), plan.Items)
	}
	for i, w := range want {
		got := plan.Items[i]
		if got.Kind != w.kind || got.Ref != w.ref || got.Action != w.action || !strings.Contains(got.Reason, w.reason) {
			t.Errorf("item %d = %+v, want %s %s %s (%q)", i, got, w.action, w.kind, w.ref, w.reason)
		}
	}

	plan.Execute()
	if plan.Count(Failed) != 0 {
		t.Fatalf("failures: %+v", plan.Items)
	}
	for _, id := range []string{"sha256:b87", "sha256:old", "sha256:dangling", "sha256:o86", "sha256:o85"} {
		if f.images[id] != nil {
			t.Errorf("%s not removed: %v", id, f.removed)
		}
	}
	for _, id := range []string{"sha256:cur", "sha256:b88", "sha256:b86", "sha256:b85", "sha256:b81", "sha256:b-86"} {
		if f.images[id] == nil {
			t.Errorf("%s removed: %v", id, f.removed)
		}
	}
	if f.removed[0] != "sha256:b87" {
		t.Errorf("official tags go before the Instance images that attribute them: %v", f.removed)
	}

	again := obsoletePlan(t, f)
	if again.Pending() != 0 {
		t.Fatalf("second run still found work: %+v", again.Items)
	}
}

func TestObsoleteImagesNeverPlansOtherOwners(t *testing.T) {
	f := versionChange(t)
	f.add("sha256:foreign", "someone-else", []string{"L87", "f"}, "someone-else:8.3.6")
	f.add("sha256:unlabelled", "", []string{"L87", "u"})
	f.add("sha256:legacy", "", []string{"L87", "g"}, nsA+":8.3.4")
	plan := obsoletePlan(t, f)
	for _, item := range plan.Items {
		if item.Kind != KindImage && item.Kind != KindBaseImage {
			t.Errorf("--obsolete-images planned a %s: %s", item.Kind, item.Ref)
		}
	}
	for _, id := range []string{"sha256:b-86", "sha256:foreign", "sha256:unlabelled"} {
		if itemFor(plan, KindImage, id) != nil {
			t.Errorf("%s planned as an Instance image", id)
		}
	}
	if item := itemFor(plan, KindImage, "sha256:legacy"); item == nil || item.Action != WouldRemove {
		t.Errorf("an image attributed by its igdev-<id8> tag alone was not planned: %+v", item)
	}
	if item := itemFor(plan, KindBaseImage, ignition+"8.3.7"); item.Action != Kept || !strings.Contains(item.Reason, "still the base of") {
		t.Errorf("an official tag other images are built on was not kept: %+v", item)
	}
}

func TestObsoleteImagesRetryAfterFailure(t *testing.T) {
	t.Run("official tag", func(t *testing.T) {
		f := versionChange(t)
		f.rmFail["sha256:b87"] = "Error response from daemon: permission denied"
		plan := obsoletePlan(t, f)
		plan.Execute()
		if item := itemFor(plan, KindBaseImage, ignition+"8.3.7"); item.Action != Failed || !strings.Contains(item.Reason, "permission denied") {
			t.Fatalf("failure not reported item by item: %+v", item)
		}
		if item := itemFor(plan, KindImage, "sha256:old"); item.Action != Kept || f.images["sha256:old"] == nil {
			t.Fatalf("the image attributing the failed tag was not held back: %+v", item)
		}
		if item := itemFor(plan, KindImage, "sha256:o86"); item.Action != Removed {
			t.Fatalf("an unrelated image was held back: %+v", item)
		}

		delete(f.rmFail, "sha256:b87")
		retry := obsoletePlan(t, f)
		retry.Execute()
		if retry.Count(Failed) != 0 || f.images["sha256:b87"] != nil || f.images["sha256:old"] != nil {
			t.Fatalf("retry did not finish: %+v", retry.Items)
		}
	})
	t.Run("instance image", func(t *testing.T) {
		f := versionChange(t)
		f.rmFail["sha256:old"] = "Error response from daemon: permission denied"
		plan := obsoletePlan(t, f)
		plan.Execute()
		if item := itemFor(plan, KindImage, "sha256:old"); item.Action != Failed {
			t.Fatalf("failure not reported: %+v", item)
		}
		delete(f.rmFail, "sha256:old")
		retry := obsoletePlan(t, f)
		retry.Execute()
		if retry.Count(Failed) != 0 || f.images["sha256:old"] != nil {
			t.Fatalf("retry did not finish: %+v", retry.Items)
		}
	})
}

func TestObsoleteBaseImageRechecksReferences(t *testing.T) {
	for _, change := range []string{"new image", "container", "engine conflict", "tag moved", "tag added"} {
		t.Run(change, func(t *testing.T) {
			f := versionChange(t)
			plan := obsoletePlan(t, f)
			switch change {
			case "new image":
				f.add("sha256:late", "", []string{"L87", "late"}, "mine:latest")
			case "container":
				f.users["sha256:b87"] = "late-container"
			case "engine conflict":
				f.rmFail["sha256:b87"] = "Error response from daemon: conflict: unable to delete sha256:b87 (must be forced) - image is referenced in multiple repositories"
			case "tag moved":
				// A re-pull moved the tag to a new image: neither is removed.
				f.images["sha256:b87"].Tags = nil
				f.add("sha256:b87-new", "", []string{"L87-new"}, ignition+"8.3.7")
			case "tag added":
				f.images["sha256:b87"].Tags = append(f.images["sha256:b87"].Tags, "mine:pinned")
			}
			plan.Execute()
			item := itemFor(plan, KindBaseImage, ignition+"8.3.7")
			if item.Action != Kept || item.Reason == "" || contains(f.removed, "sha256:b87") || contains(f.removed, "sha256:b87-new") {
				t.Fatalf("item = %+v, removed = %v", item, f.removed)
			}
			// The Instance image stays with it, so a retry can still attribute it.
			if image := itemFor(plan, KindImage, "sha256:old"); image.Action != Kept || f.images["sha256:old"] == nil {
				t.Fatalf("the image attributing the kept tag was not held back: %+v", image)
			}
		})
	}
}

// A builder that records parent images refuses to delete one a child still
// names; nothing is forced, so the child goes first and the official image is
// tried again after it, in the same run.
func TestObsoleteBaseImageWithDependentChild(t *testing.T) {
	f := versionChange(t)
	plan, fault := Build(Options{Root: "/repo", Namespace: nsA, ObsoleteImages: true, IgnitionVersion: "8.3.8",
		Docker: func(args ...string) (string, string, error) {
			if len(args) == 3 && args[0] == "image" && args[1] == "rm" && args[2] == "sha256:b87" && f.images["sha256:old"] != nil {
				return "", "Error response from daemon: conflict: unable to delete sha256:b87 (cannot be forced) - image has dependent child images", errors.New("exit status 1")
			}
			return f.run(args...)
		}})
	if fault != nil {
		t.Fatal(fault)
	}
	plan.Execute()
	if item := itemFor(plan, KindBaseImage, ignition+"8.3.7"); item.Action != Removed || f.images["sha256:b87"] != nil {
		t.Fatalf("official image not retried after its child: %+v", item)
	}
	if item := itemFor(plan, KindImage, "sha256:old"); item.Action != Removed {
		t.Fatalf("child image not removed: %+v", item)
	}
	if plan.Count(Failed) != 0 {
		t.Fatalf("failures: %+v", plan.Items)
	}
}

// An official image with several tags is kept: an unforced removal by ID would
// be refused, and untagging would act on mutable references.
func TestObsoleteBaseImageWithSeveralTagsIsKept(t *testing.T) {
	f := versionChange(t)
	f.images["sha256:b87"].Tags = []string{ignition + "8.3", ignition + "8.3.7"}
	plan := obsoletePlan(t, f)
	for _, ref := range []string{ignition + "8.3", ignition + "8.3.7"} {
		if item := itemFor(plan, KindBaseImage, ref); item == nil || item.Action != Kept || !strings.Contains(item.Reason, "several tags") {
			t.Errorf("%s: %+v", ref, item)
		}
	}
	plan.Execute()
	if f.images["sha256:b87"] == nil || f.images["sha256:old"] != nil {
		t.Fatalf("removed = %v", f.removed)
	}
}

// A dangling Instance image left by a rebuild carries no tag: its layers still
// attribute the official image it was built from.
func TestObsoleteDanglingImageAttributesItsBase(t *testing.T) {
	f := versionChange(t)
	f.add("sha256:b84", "", []string{"L84"}, ignition+"8.3.4")
	f.add("sha256:d84", nsA, []string{"L84", "d"})
	plan := obsoletePlan(t, f)
	if item := itemFor(plan, KindBaseImage, ignition+"8.3.4"); item.Action != WouldRemove || !strings.Contains(item.Reason, "sha256:b84") {
		t.Fatalf("item = %+v", item)
	}
	plan.Execute()
	if f.images["sha256:b84"] != nil || f.images["sha256:d84"] != nil {
		t.Fatalf("removed = %v", f.removed)
	}
}

// A base image whose layers cannot be read could be under any image: keep it.
func TestObsoleteBaseImageWithoutLayersIsKept(t *testing.T) {
	f := versionChange(t)
	f.images["sha256:b87"].layers = nil
	item := itemFor(obsoletePlan(t, f), KindBaseImage, ignition+"8.3.7")
	if item.Action != Kept || !strings.Contains(item.Reason, "layers are unknown") {
		t.Fatalf("item = %+v", item)
	}
}

// Without a namespace every unattributed image would match, and without a
// version the current Instance image would be planned: both are refused.
func TestObsoleteImagesRefuseEmptyScope(t *testing.T) {
	f := versionChange(t)
	for _, opts := range []Options{
		{ObsoleteImages: true, IgnitionVersion: "8.3.8", Docker: f.run},
		{ObsoleteImages: true, Namespace: nsA, Docker: f.run},
	} {
		if plan, fault := Build(opts); fault == nil || len(plan.Items) != 0 {
			t.Fatalf("Build(%+v) = %+v, %v", opts, plan.Items, fault)
		}
	}
}
