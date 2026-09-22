package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every command that can print help carries the machine-facing section: it is
// the part of the reference an agent reads, and a command that silently lacks it
// is the gap this ticket exists to close.
func TestEveryCommandHasAgentUsage(t *testing.T) {
	root := NewDocumentationTree()

	seen := map[string]bool{}
	walkCommands(root, func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		path := cmd.CommandPath()
		usage, ok := agentUsage[path]
		if !ok || strings.TrimSpace(usage) == "" {
			t.Errorf("%s has no Agent usage section", path)
			return
		}
		seen[path] = true
		if !strings.Contains(helpText(cmd), "Agent usage: ") {
			t.Errorf("%s: help text does not render the Agent usage section", path)
		}
	})

	// An entry that matches no command is dead text: it would never be printed.
	for path := range agentUsage {
		if !seen[path] {
			t.Errorf("agentUsage has an entry for %q, which is not a command", path)
		}
	}
}

// The generated agent-context page is only as good as this table, so every name
// in it is checked against the envelope's own struct tags. A field added to the
// data struct without a table row fails here, and so does a row naming a field
// that does not exist.
func TestAgentContextFieldsMatchData(t *testing.T) {
	tags := jsonTags(reflect.TypeOf(agentContextData{}))

	documented := map[string]bool{}
	for _, field := range AgentContextFields {
		if field.Type == "" || field.Description == "" {
			t.Errorf("field %q is missing its type or description", field.Name)
		}
		top := field.Name
		if i := strings.Index(top, "."); i >= 0 {
			top = top[:i]
		}
		if !tags[top] {
			t.Errorf("field %q does not name a member of agentContextData", field.Name)
		}
		documented[top] = true
	}
	for tag := range tags {
		if !documented[tag] {
			t.Errorf("agentContextData member %q is not documented in AgentContextFields", tag)
		}
	}
}

// jsonTags returns the JSON names of a struct's top-level fields.
func jsonTags(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[strings.Split(tag, ",")[0]] = true
	}
	return out
}

// walkCommands visits cmd and every descendant, parents before children.
func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, child := range cmd.Commands() {
		walkCommands(child, visit)
	}
}
