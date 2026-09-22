package docsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/cli"
)

// The committed reference is the generator's output, so it is compared against a
// fresh render: a command definition that changed without the reference changing
// fails here, which is what makes the docs unable to drift from the CLI. Run
// `make reference` to regenerate.
func TestReferenceMatchesGenerator(t *testing.T) {
	files := Render()
	if len(files) == 0 {
		t.Fatal("Render produced no pages")
	}
	for name, content := range files {
		committed, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Errorf("%s: %v (run `make reference`)", name, err)
			continue
		}
		if string(committed) != content {
			t.Errorf("%s is not what the command definitions render (run `make reference`)", name)
		}
	}
}

// Every command page carries the machine-facing section, so the reference and
// `--help` stay the same document.
func TestRenderedPagesCarryAgentUsage(t *testing.T) {
	for name, content := range Render() {
		if !strings.Contains(content, "## Agent usage") {
			t.Errorf("%s has no Agent usage section", name)
		}
	}
}

// The field documentation of `agent context --json` is generated from the table
// in internal/cli, so it must appear on that command's page.
func TestAgentContextPageDocumentsFields(t *testing.T) {
	page := Render()[filepath.Join(Dir, "agent-context.md")]
	if page == "" {
		t.Fatalf("%s was not rendered", filepath.Join(Dir, "agent-context.md"))
	}
	for _, field := range cli.AgentContextFields {
		if !strings.Contains(page, "`"+field.Name+"`") {
			t.Errorf("the agent context page does not document %q", field.Name)
		}
	}
}
