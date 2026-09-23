package agentskill

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// frontmatter parses the YAML-ish frontmatter block between the leading "---"
// delimiters, returning each key's value.
func frontmatter(t *testing.T, doc string) map[string]string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("SKILL.md does not open with a frontmatter fence:\n%s", firstLines(doc, 4))
	}
	out := map[string]string{}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return out
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	t.Fatalf("SKILL.md frontmatter is not closed:\n%s", firstLines(doc, 8))
	return nil
}

// The frontmatter is the installation contract an agent reads: it must name the
// skill and carry the CLI Contract Version this binary speaks, so the workflow
// guidance can never disagree with the tool that installed it. A contract bump
// that forgets to update the asset fails here.
func TestFrontmatterCarriesTheContractVersion(t *testing.T) {
	doc := string(Content())
	fm := frontmatter(t, doc)
	if fm["name"] != Name {
		t.Errorf("frontmatter name = %q, want %q", fm["name"], Name)
	}
	if fm["version"] != contract.Version {
		t.Errorf("frontmatter version = %q, want the CLI Contract Version %q", fm["version"], contract.Version)
	}
	if Version() != contract.Version {
		t.Errorf("Version() = %q, want %q", Version(), contract.Version)
	}
	if strings.TrimSpace(fm["description"]) == "" {
		t.Error("frontmatter carries no description")
	}
}

// The body is the thin 8-rule workflow the spec fixes: status first, the init
// vs setup boundary, human-only consent, check-first validation, a Gateway only
// when runtime matters, never editing .igdev/, never bypassing a preflight
// fault, and always speaking JSON. Each rule is asserted by a phrase an agent
// reads, so a rewrite that drops one is caught.
func TestBodyTeachesTheFrozenWorkflow(t *testing.T) {
	doc := string(Content())
	for _, want := range []string{
		"**Status first.**",
		"Keep init and setup separate.",
		"Never accept a legal term.",
		"Validate before you run.",
		"Start a Gateway only when runtime matters.",
		"Never edit `.igdev/`.",
		"Never bypass a preflight fault.",
		"Speak JSON.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKILL.md is missing the workflow rule %q", want)
		}
	}
}

// The skill is workflow, not a catalog: it must not duplicate the embedded
// capability data, so it stays thin and cannot drift from the Core Catalog.
func TestBodyCarriesNoCapabilityCatalog(t *testing.T) {
	doc := string(Content())
	for _, forbidden := range []string{"system.perspective", "com.inductiveautomation.", "/data/"} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("SKILL.md carries catalog data (%q), which belongs to the Core Catalog", forbidden)
		}
	}
}

// maxEntryLines caps SKILL.md. Agents load the entry whole on every activation,
// so detail belongs in references/, which they read on demand.
const maxEntryLines = 80

func TestEntryStaysShort(t *testing.T) {
	lines := strings.Count(string(Content()), "\n")
	if lines > maxEntryLines {
		t.Errorf("SKILL.md has %d lines, the cap is %d: move detail into references/", lines, maxEntryLines)
	}
}

// Every references/ path SKILL.md names must be a file the skill installs, and
// the generated command and error references must be there.
func TestEntryRoutesToEmbeddedReferences(t *testing.T) {
	installed := Files()
	for _, want := range []string{FileName, "references/README.md", "references/errors.md", "references/gateway-up.md"} {
		if _, ok := installed[want]; !ok {
			t.Errorf("the skill does not embed %s", want)
		}
	}
	for _, path := range regexp.MustCompile("`(references/[a-z-]+\\.md)`").FindAllStringSubmatch(string(Content()), -1) {
		if _, ok := installed[path[1]]; !ok {
			t.Errorf("SKILL.md routes to %s, which the skill does not embed", path[1])
		}
	}
}

func firstLines(s string, n int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
