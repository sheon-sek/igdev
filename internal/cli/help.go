package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/contract"
)

// helpData is the `data` member of a machine-mode help result.
type helpData struct {
	Command string `json:"command"`
	Help    string `json:"help"`
	// Section names the help topic: root for the command list, or the command
	// whose reference is shown.
	Section string `json:"section"`
}

// printHelp renders cobra's help text in the dialect the caller chose: the text
// itself on stdout for humans, the same text inside the envelope for agents.
func (a *App) printHelp(cmd *cobra.Command, _ []string) {
	text := helpText(cmd)
	if !a.wantsJSON() {
		fmt.Fprint(a.Stdout, text)
		return
	}
	section := "root"
	if cmd != nil && cmd.Name() != "igdev" {
		section = cmd.Name()
	}
	a.writeEnvelope(contract.Success(helpData{Command: commandPath(cmd), Help: text, Section: section}))
}

// helpText reproduces cobra's default help template: the long description (or
// short one), the Agent usage section, then the usage block, which carries flags
// and examples. The machine-facing section is rendered from the one table in
// agentusage.go, so --help and the generated reference cannot disagree.
func helpText(cmd *cobra.Command) string {
	var b strings.Builder
	body := strings.TrimRight(cmd.Long, " \t\r\n")
	if body == "" {
		body = strings.TrimRight(cmd.Short, " \t\r\n")
	}
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	if usage := AgentUsage(cmd); usage != "" {
		b.WriteString("Agent usage: ")
		b.WriteString(usage)
		b.WriteString("\n\n")
	}
	b.WriteString(strings.TrimRight(cmd.UsageString(), " \t\r\n"))
	b.WriteString("\n")
	return b.String()
}

func commandPath(cmd *cobra.Command) string {
	if cmd == nil {
		return "igdev"
	}
	return cmd.CommandPath()
}
