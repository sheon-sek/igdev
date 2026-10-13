package cleanup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestImageInventoryScopesAndProtections(t *testing.T) {
	images := map[string]Image{
		"sha256:old":     {ID: "sha256:old", Project: nsA},
		"sha256:new":     {ID: "sha256:new", Project: nsA, Tags: []string{nsA + ":8.3.8", nsA + ":latest"}},
		"sha256:used":    {ID: "sha256:used", Project: nsA},
		"sha256:other":   {ID: "sha256:other", Project: nsB},
		"sha256:foreign": {ID: "sha256:foreign", Project: "foreign", Tags: []string{nsA + ":foreign"}},
		"sha256:unknown": {ID: "sha256:unknown"},
		"sha256:shared":  {ID: "sha256:shared", Project: nsA, Tags: []string{nsA + ":shared", "outside:latest"}},
		"sha256:legacy":  {ID: "sha256:legacy", Tags: []string{nsA + ":legacy"}},
	}
	failed := true
	var removed []string
	run := func(args ...string) (string, string, error) {
		switch {
		case args[0] == "image" && args[1] == "ls":
			ids := sortedKeysImage(images)
			if _, ok := images["sha256:new"]; ok {
				ids = append(ids, "sha256:new")
			}
			return strings.Join(ids, "\n"), "", nil
		case args[0] == "image" && args[1] == "inspect":
			i := images[args[2]]
			raw, _ := json.Marshal([]map[string]any{{"Id": i.ID, "RepoTags": i.Tags, "Config": map[string]any{"Labels": map[string]string{labelProject: i.Project}}}})
			return string(raw), "", nil
		case args[0] == "ps" && len(args) > 2 && args[2] == "--filter":
			if args[3] == "ancestor=sha256:used" {
				return "foreign-container", "", nil
			}
		case args[0] == "image" && args[1] == "rm":
			if len(args) != 3 {
				t.Fatalf("unsafe removal: %v", args)
			}
			if args[2] == "sha256:old" && failed {
				return "", "permission denied", errors.New("exit 1")
			}
			removed = append(removed, args[2])
			delete(images, args[2])
		}
		return "", "", nil
	}
	for _, machine := range []bool{false, true} {
		p, f := Build(Options{Namespace: nsA, Machine: machine, Docker: run})
		if f != nil {
			t.Fatal(f)
		}
		counts := map[string]int{}
		for _, i := range p.Items {
			if i.Kind == KindImage {
				counts[i.Ref]++
			}
		}
		for _, id := range []string{"sha256:old", "sha256:new", "sha256:used", "sha256:shared", "sha256:legacy"} {
			if counts[id] != 1 {
				t.Errorf("%s count=%d", id, counts[id])
			}
		}
		if (counts["sha256:other"] == 1) != machine || counts["sha256:foreign"] != 0 || counts["sha256:unknown"] != 0 {
			t.Fatalf("scope inventory: %v", counts)
		}
		for _, id := range []string{"sha256:used", "sha256:shared"} {
			if !contains(refs(p, Kept), id) {
				t.Errorf("%s not kept", id)
			}
		}
	}
	p, _ := Build(Options{Namespace: nsA, Docker: run})
	p.Execute()
	if !contains(refs(p, Failed), "sha256:old") {
		t.Fatal("failure not reported")
	}
	failed = false
	p, _ = Build(Options{Namespace: nsA, Docker: run})
	p.Execute()
	if !contains(removed, "sha256:old") {
		t.Fatal("failed image not retryable")
	}
	if contains(removed, "sha256:used") || contains(removed, "sha256:shared") {
		t.Fatal("protected image removed")
	}
}

func sortedKeysImage(images map[string]Image) []string {
	set := map[string]bool{}
	for id := range images {
		set[id] = true
	}
	return sortedKeys(set)
}

func TestImageRemovalRechecksReferences(t *testing.T) {
	for _, protection := range []string{"tag", "container", "ownership", "query failure", "engine conflict"} {
		t.Run(protection, func(t *testing.T) {
			changed := false
			removed := false
			image := Image{ID: "sha256:owned", Project: nsA, Tags: []string{nsA + ":current"}}
			run := func(args ...string) (string, string, error) {
				switch args[0] {
				case "image":
					if args[1] == "inspect" {
						current := image
						if changed && protection == "tag" {
							current.Tags = append(current.Tags, "external:latest")
						}
						if changed && protection == "ownership" {
							current.Project = nsB
						}
						raw, _ := json.Marshal([]map[string]any{{"Id": current.ID, "RepoTags": current.Tags, "Config": map[string]any{"Labels": map[string]string{labelProject: current.Project}}}})
						return string(raw), "", nil
					}
					if args[1] == "rm" {
						if protection == "engine conflict" {
							return "", "conflict: image is referenced in multiple repositories", errors.New("exit 1")
						}
						removed = true
					}
				case "ps":
					if changed && protection == "container" {
						return "late-container", "", nil
					}
					if changed && protection == "query failure" {
						return "", "daemon unavailable", errors.New("exit 1")
					}
				}
				return "", "", nil
			}
			item := imageItem(image, run, nil)
			changed = true
			Plan{Items: []*Item{item}}.Execute()
			want := Kept
			if protection == "query failure" {
				want = Failed
			}
			if removed || item.Action != want || item.Reason == "" {
				t.Fatalf("removed=%v, item=%+v", removed, item)
			}
		})
	}
}

func TestImageInventoryInspectionFailure(t *testing.T) {
	for _, out := range []string{"invalid json", `[{"Id":"sha256:other"}]`} {
		run := func(args ...string) (string, string, error) {
			if args[1] == "ls" {
				return "sha256:owned", "", nil
			}
			return out, "", nil
		}
		if _, fault := InventoryImages(run); fault == nil {
			t.Fatalf("accepted %s", out)
		}
	}
}

// An Instance's own Gateway container uses its image at plan time; the same
// plan removes that container first, so the image is removed, not kept.
func TestImageUsedOnlyByOwnContainerIsRemoved(t *testing.T) {
	gone := false
	var removed []string
	image := Image{ID: "sha256:img", Project: nsA, Tags: []string{nsA + ":8.3.8", nsA + ":latest"}}
	run := func(args ...string) (string, string, error) {
		switch {
		case args[0] == "ps" && args[1] == "--all" && len(args) > 2 && args[2] == "--filter":
			if !gone {
				return "gw", "", nil
			}
			return "", "", nil
		case args[0] == "ps":
			if gone {
				return "", "", nil
			}
			return "gw\t" + nsA + "\t\t", "", nil
		case args[0] == "rm":
			gone = true
		case args[0] == "image" && args[1] == "ls":
			return image.ID, "", nil
		case args[0] == "image" && args[1] == "inspect":
			raw, _ := json.Marshal([]map[string]any{{"Id": image.ID, "RepoTags": image.Tags, "Config": map[string]any{"Labels": map[string]string{labelProject: image.Project}}}})
			return string(raw), "", nil
		case args[0] == "image" && args[1] == "rm":
			removed = append(removed, args[2])
		}
		return "", "", nil
	}
	p, f := Build(Options{Namespace: nsA, Docker: run})
	if f != nil {
		t.Fatal(f)
	}
	if !contains(refs(p, WouldRemove), "sha256:img") {
		t.Fatalf("own image not planned: %+v", p.Items)
	}
	p.Execute()
	if !contains(refs(p, Removed), "sha256:img") || !contains(removed, nsA+":latest") || !contains(removed, "sha256:img") {
		t.Fatalf("removed=%v items=%+v", removed, p.Items)
	}
}

func TestImageInventoryToleratesConcurrentRemoval(t *testing.T) {
	run := func(args ...string) (string, string, error) {
		if args[1] == "ls" {
			return "sha256:gone", "", nil
		}
		return "", "Error: No such image: sha256:gone", errors.New("exit 1")
	}
	images, fault := InventoryImages(run)
	if fault != nil || len(images) != 0 {
		t.Fatalf("images=%v fault=%v", images, fault)
	}
}
