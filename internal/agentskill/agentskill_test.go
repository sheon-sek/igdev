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

// The frontmatter has to be YAML that GitHub and skill loaders parse: a plain
// (unquoted) scalar may not contain ": ", which YAML reads as a nested mapping.
func TestFrontmatterPlainValuesAreValidYAML(t *testing.T) {
	doc := string(Content())
	frontmatter(t, doc)
	for _, line := range strings.Split(doc, "\n")[1:] {
		if strings.TrimSpace(line) == "---" {
			return
		}
		_, value, ok := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		if !ok || strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "'") {
			continue
		}
		if strings.Contains(value, ": ") || strings.Contains(value, " #") {
			t.Errorf("frontmatter line %q needs quoting to be valid YAML", line)
		}
	}
}

// The body teaches the three layers and the five steps, each step ending on a
// checkable completion criterion, plus the Consent guardrail. Each piece is
// asserted by a phrase an agent reads, so a rewrite that drops one is caught.
func TestBodyTeachesLayersStepsAndConsent(t *testing.T) {
	doc := string(Content())
	for _, want := range []string{
		"## Three layers", "Environment", "Operations on the Gateway", "Knowledge and checks",
		"**Orient.**", "**Look it up.**", "**Check.**", "**Gateway, when behaviour depends on the Ignition runtime**", "**Finish.**",
		"## Consent", "IGDEV_E_CONSENT_REQUIRED",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKILL.md is missing %q", want)
		}
	}
	if got := strings.Count(doc, "Done when"); got < 6 {
		t.Errorf("SKILL.md has %d completion criteria (\"Done when\"), want one per step and one for Consent", got)
	}
}

// The description is a trigger: it says when to load the skill, one condition
// per branch, rather than summarising the body.
func TestDescriptionNamesTheTriggers(t *testing.T) {
	description := frontmatter(t, string(Content()))["description"]
	for _, want := range []string{"Use when", "igdev.toml", "Gateway", "system.*", "SDK module", "MCP Tools", "AI agent"} {
		if !strings.Contains(description, want) {
			t.Errorf("the description does not name the trigger %q: %s", want, description)
		}
	}
}

// Prohibitions are kept for the one guardrail that cannot be stated positively:
// Consent. Every other rule is a positive instruction.
func TestEntryKeepsNeverForConsentOnly(t *testing.T) {
	section := ""
	for _, line := range strings.Split(string(Content()), "\n") {
		if strings.HasPrefix(line, "## ") {
			section = line
		}
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "never") || strings.Contains(lower, "do not") || strings.Contains(lower, "don't")) &&
			section != "## Consent" {
			t.Errorf("SKILL.md prohibits outside the Consent section (%s): %q", section, line)
		}
	}
}

// Every Gateway interaction goes through an igdev verb: the skill names the
// verbs of each layer and teaches no raw docker or token-carrying curl workflow.
func TestBodyTeachesTheGatewayVerbs(t *testing.T) {
	doc := string(Content())
	for _, want := range []string{
		"igdev gateway ensure", "igdev gateway api", "igdev gateway exec", "igdev gateway data",
		"igdev module install", "igdev restart", "igdev project import", "igdev lookup", "igdev check",
		"igdev cleanup",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKILL.md does not teach %q", want)
		}
	}
	for _, raw := range []string{"docker exec", "docker cp", "docker inspect", "X-Ignition-API-Token"} {
		if strings.Contains(doc, raw) {
			t.Errorf("SKILL.md teaches %q instead of the igdev verb", raw)
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
// the generated command and error references and the hand-written guides must be
// there.
func TestEntryRoutesToEmbeddedReferences(t *testing.T) {
	installed := Files()
	for _, want := range []string{
		FileName, "references/README.md", "references/errors.md", "references/gateway-up.md",
		"references/guides/scenario-sdk-module.md", "references/guides/scenario-mcp-tools.md",
		"references/guides/scenario-ai-agent.md", "references/guides/lookup.md", "references/guides/cleanup.md",
	} {
		if _, ok := installed[want]; !ok {
			t.Errorf("the skill does not embed %s", want)
		}
	}
	for _, path := range referencePath.FindAllStringSubmatch(string(Content()), -1) {
		if _, ok := installed[path[1]]; !ok {
			t.Errorf("SKILL.md routes to %s, which the skill does not embed", path[1])
		}
	}
}

// referencePath matches a references/ page named in backquotes.
var referencePath = regexp.MustCompile("`(references/[a-z/-]+\\.md)`")

// The guides are hand-written: they live in references/guides/, where the
// generator neither writes nor checks, and none carries the generated header.
// Each is a sequence of steps with completion criteria, routes only to pages the
// skill installs, and links to the command reference instead of restating flags.
func TestGuidesAreHandWrittenStepsWithCompletionCriteria(t *testing.T) {
	installed := Files()
	guides := 0
	for path, raw := range installed {
		if !strings.HasPrefix(path, "references/guides/") {
			continue
		}
		guides++
		page := string(raw)
		if strings.HasPrefix(page, "<!-- Generated") {
			t.Errorf("%s carries the generated header; the guides are hand-written", path)
		}
		if !strings.Contains(page, "Done when") {
			t.Errorf("%s has no completion criterion (\"Done when\")", path)
		}
		if strings.Contains(page, "## Options") {
			t.Errorf("%s restates a command's options; link to the command reference instead", path)
		}
		for _, ref := range referencePath.FindAllStringSubmatch(page, -1) {
			if _, ok := installed[ref[1]]; !ok {
				t.Errorf("%s routes to %s, which the skill does not embed", path, ref[1])
			}
		}
	}
	if guides != 5 {
		t.Errorf("the skill embeds %d guides, want 5 (three scenarios, lookup and cleanup)", guides)
	}
}

func firstLines(s string, n int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
