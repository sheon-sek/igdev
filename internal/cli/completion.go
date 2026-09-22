package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/contract"
)

// shells are the shells igdev writes completion scripts for.
var shells = []string{"bash", "zsh", "fish", "powershell"}

// newCompletionCmd owns the completion surface instead of cobra's default so a
// missing or unsupported shell name fails as a contract error.
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "completion <shell>",
		Short: "Print the shell completion script for igdev",
		Long: `completion writes the autocompletion script for one shell on stdout. Load it from your
shell rc file, for example:

  # ~/.bashrc
  source <(igdev completion bash)

  # ~/.zshrc  (after: mkdir -p ~/.zfunc && adding "autoload -Uz compinit" && compinit)
  igdev completion zsh > ~/.zfunc/_igdev

Supported shells: ` + strings.Join(shells, ", ") + `. The script is data, not an envelope, so it is
written verbatim and never depends on --json.`,
		Example: "  igdev completion bash\n  igdev completion zsh > \"${fpath[1]}/_igdev\"",
		Args: func(_ *cobra.Command, args []string) error {
			switch len(args) {
			case 0:
				return missingArgument("completion", "shell", "igdev completion bash",
					"print the bash completion script")
			case 1:
				if !knownShell(args[0]) {
					return contract.UsageFault(
						fmt.Sprintf("unsupported shell %q; supported shells: %s", args[0], strings.Join(shells, ", ")),
						contract.Remediation{Command: "igdev completion bash", Why: "print the bash completion script"})
				}
				return nil
			default:
				return extraArguments("completion", 1, args)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var err error
			switch args[0] {
			case "bash":
				err = root.GenBashCompletionV2(out, true)
			case "zsh":
				err = root.GenZshCompletion(out)
			case "fish":
				err = root.GenFishCompletion(out, true)
			case "powershell":
				err = root.GenPowerShellCompletionWithDesc(out)
			}
			return err
		},
	}
}

func knownShell(name string) bool {
	for _, s := range shells {
		if s == name {
			return true
		}
	}
	return false
}
