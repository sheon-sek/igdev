package cleanup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheon-sek/igdev/internal/agentskill"
	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/textdiff"
)

// diffContext matches the context `igdev init` prints around a tracked change.
const diffContext = 3

// skillRoots are the harness directories a skills root sits under, in the order
// `agent skill-install` writes them.
var skillRoots = []string{".agents", ".claude"}

// deinitItems are what `igdev init` and `agent skill-install --scope repo` wrote
// into the repository, plus the tracked content a person wrote, kept. The
// .gitignore entry stays: it is harmless and the owner asked that cleanup leave
// .gitignore alone.
func deinitItems(opts Options) []*Item {
	var items []*Item
	contractPath := filepath.Join(opts.Root, project.ContractFile)
	if exists(contractPath) {
		items = append(items, &Item{Kind: KindContract, Ref: contractPath, Action: WouldRemove,
			remove: func() error { return removeFile(contractPath) }})
	}
	if item := agentsBlockItem(opts.Root, opts.Agents); item != nil {
		items = append(items, item)
	}
	items = append(items, skillItems(opts.Root, true)...)

	if doc, err := project.ParseDoc(opts.ContractTOML, contractPath); err == nil {
		paths := append(append([]string{}, doc.Catalog.OverlayPaths...), doc.Gateway.Seed...)
		for _, rel := range paths {
			path := filepath.Join(opts.Root, filepath.FromSlash(rel))
			if exists(path) {
				items = append(items, &Item{Kind: KindUserContent, Ref: path, Action: Kept,
					Reason: "tracked project content the contract points at; igdev did not write it"})
			}
		}
	}
	return items
}

// agentsBlockItem removes the managed block from AGENTS.md, and the file itself
// when nothing else is left in it.
func agentsBlockItem(root string, block AgentsBlock) *Item {
	if block.File == "" || block.Start == "" {
		return nil
	}
	path := filepath.Join(root, block.File)
	existing, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(existing), block.Start) {
		return nil
	}
	updated := withoutAgentsBlock(existing, block.Start, block.End)
	item := &Item{Kind: KindAgentsBlock, Ref: path, Action: WouldRemove}
	if blank(updated) {
		item.Diff = textdiff.Unified(block.File, existing, nil, diffContext)
		item.remove = func() error { return removeFile(path) }
		return item
	}
	item.Diff = textdiff.Unified(block.File, existing, updated, diffContext)
	item.remove = func() error { return rewrite(path, updated) }
	return item
}

// withoutAgentsBlock is the inverse of init's upsert: the marked span goes, and
// so does the blank line init put in front of it when it appended the block. A
// block with no end marker ran to the end of the file, as init reads it too.
func withoutAgentsBlock(existing []byte, start, end string) []byte {
	lines := strings.SplitAfter(string(existing), "\n")
	first, last := -1, len(lines)-1
	for i, line := range lines {
		if first < 0 {
			if strings.Contains(line, start) {
				first = i
			}
			continue
		}
		if strings.Contains(line, end) {
			last = i
			break
		}
	}
	if first < 0 {
		return existing
	}
	before := strings.Join(lines[:first], "")
	after := strings.Join(lines[last+1:], "")
	if after == "" && strings.HasSuffix(before, "\n\n") {
		before = strings.TrimSuffix(before, "\n")
	}
	return []byte(before + after)
}

// skillItems are the skill installs under base: SKILL.md and references/ are
// igdev's, and anything else beside them stays. A repository install also takes
// the skills and harness directories it leaves empty; a global install leaves
// ~/.claude and ~/.agents alone, since they belong to the harnesses.
func skillItems(base string, repo bool) []*Item {
	var items []*Item
	if base == "" {
		return nil
	}
	for _, harness := range skillRoots {
		dir := filepath.Join(base, harness, "skills", agentskill.Dir)
		entry := filepath.Join(dir, agentskill.FileName)
		refs := filepath.Join(dir, agentskill.ReferencesDir)
		if !exists(entry) && !exists(refs) {
			continue
		}
		harnessDir := filepath.Join(base, harness)
		items = append(items, &Item{Kind: KindSkill, Ref: dir, Action: WouldRemove, remove: func() error {
			if err := removeFile(entry); err != nil {
				return err
			}
			if err := os.RemoveAll(refs); err != nil {
				return errorf(refs, err)
			}
			removeIfEmpty(dir)
			if repo {
				removeIfEmpty(filepath.Join(harnessDir, "skills"))
				removeIfEmpty(harnessDir)
			}
			return nil
		}})
	}
	return items
}

// ProfileMarker is the line packaging/install.sh writes above the PATH entry it
// adds to a login profile.
const ProfileMarker = "# igdev: install prefix on PATH (added by igdev install.sh)"

// profileNames are the login profiles install.sh may write, in its own order.
var profileNames = []string{".bash_profile", ".bash_login", ".profile"}

// prefixPattern reads the install prefix back out of the PATH line install.sh
// writes: case ":$PATH:" in *":<prefix>:"*) ...
var prefixPattern = regexp.MustCompile(`\*":(.*?):"\*`)

// uninstallItems are the binary and the login-profile PATH entries install.sh
// added. A binary outside every install.sh prefix was installed some other way
// (a package manager, go install) and is left to that tool.
func uninstallItems(opts Options) []*Item {
	var items []*Item
	prefixes := map[string]bool{}
	if opts.Home != "" {
		prefixes[filepath.Join(opts.Home, ".local", "bin")] = true
	}
	for _, name := range profileNames {
		path := filepath.Join(opts.Home, name)
		existing, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(existing), ProfileMarker) {
			continue
		}
		updated, prefix := withoutProfileEntry(existing)
		if prefix != "" {
			prefixes[filepath.Clean(prefix)] = true
		}
		items = append(items, &Item{Kind: KindProfile, Ref: path, Action: WouldRemove,
			Diff:   textdiff.Unified(name, existing, updated, diffContext),
			remove: func() error { return rewrite(path, updated) }})
	}

	exe := opts.Executable
	if exe == "" {
		return items
	}
	binary := &Item{Kind: KindBinary, Ref: exe, Action: Kept,
		Reason: "not under an install.sh prefix; remove it with the tool that installed it"}
	if prefixes[filepath.Dir(exe)] {
		binary.Action, binary.Reason = WouldRemove, ""
		binary.remove = func() error { return removeFile(exe) }
	}
	// The binary goes last: everything before it ran from it.
	return append(items, binary)
}

// withoutProfileEntry removes install.sh's block — the blank line it writes
// first, the marker, its PATH line and `export PATH` — and reports the prefix
// the PATH line named.
func withoutProfileEntry(existing []byte) ([]byte, string) {
	lines := strings.SplitAfter(string(existing), "\n")
	var out []string
	prefix := ""
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") != ProfileMarker {
			out = append(out, lines[i])
			continue
		}
		if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
			out = out[:n-1]
		}
		for j := 0; j < 2 && i+1 < len(lines); j++ {
			next := strings.TrimSpace(lines[i+1])
			if !strings.HasPrefix(next, "case \":$PATH:\"") && next != "export PATH" {
				break
			}
			if m := prefixPattern.FindStringSubmatch(next); m != nil {
				prefix = m[1]
			}
			i++
		}
	}
	return []byte(strings.Join(out, "")), prefix
}

// rewrite replaces path's content, keeping its permission bits.
func rewrite(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicfile.Write(path, data, mode, 0o755); err != nil {
		return errorf(path, err)
	}
	return nil
}

// removeFile removes one file; one already gone is the goal reached.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errorf(path, err)
	}
	return nil
}
