package docsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/cli"
	"github.com/sheon-sek/igdev/internal/contract"
)

// The committed reference is the generator's output, so every copy of it is
// compared against a fresh render: a command definition that changed without the
// reference changing fails here, which is what makes the docs and the Agent
// Skill unable to drift from the CLI. Run `make reference` to regenerate.
func TestReferenceMatchesGenerator(t *testing.T) {
	files := Render()
	if len(files) == 0 {
		t.Fatal("Render produced no pages")
	}
	for _, dir := range Dirs {
		for name, content := range files {
			path := filepath.Join(dir, name)
			committed, err := os.ReadFile(filepath.Join("..", "..", path))
			if err != nil {
				t.Errorf("%s: %v (run `make reference`)", path, err)
				continue
			}
			if string(committed) != content {
				t.Errorf("%s is not what the command definitions render (run `make reference`)", path)
			}
		}
	}
}

// The error reference lists every code, so an agent never has to guess what a
// code means.
func TestErrorsPageListsEveryCode(t *testing.T) {
	page := Render()[errorsPage]
	for _, doc := range contract.CodeDocs {
		if !strings.Contains(page, "`"+string(doc.Code)+"`") {
			t.Errorf("%s does not list %s", errorsPage, doc.Code)
		}
	}
}

// Every command page carries the machine-facing section, so the reference and
// `--help` stay the same document.
func TestRenderedPagesCarryAgentUsage(t *testing.T) {
	for name, content := range Render() {
		if name == errorsPage {
			continue
		}
		if !strings.Contains(content, "## Agent usage") {
			t.Errorf("%s has no Agent usage section", name)
		}
	}
}

// The field documentation of `agent context --json` is generated from the table
// in internal/cli, so it must appear on that command's page.
func TestAgentContextPageDocumentsFields(t *testing.T) {
	page := Render()["agent-context.md"]
	if page == "" {
		t.Fatal("agent-context.md was not rendered")
	}
	for _, field := range cli.AgentContextFields {
		if !strings.Contains(page, "`"+field.Name+"`") {
			t.Errorf("the agent context page does not document %q", field.Name)
		}
	}
}
