package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/buildinfo"
	"github.com/sheon-sek/igdev/internal/contract"
)

// Version is the release semver compiled into this binary.
func Version() string { return buildinfo.Version }

// ContractVersion is the CLI Contract Version this binary speaks.
func ContractVersion() string { return contract.Version }

const rootLong = `igdev is the Ignition local-development toolchain: one global binary that
owns the engine and the Ignition domain knowledge, while each repository keeps only a
thin tracked Project Contract (igdev.toml) plus disposable local state in .igdev/.

Lifecycle verbs: init creates the Project Contract, setup materializes this checkout.
Every project command first passes the Gate: discover Project Root, validate contract
schema, validate Setup Stamp, check command prerequisites.

Agent usage: pass --json for the machine contract. stdout is then a single JSON
envelope {ok, contract, code, message, remediation, data}; progress and notices go to
stderr. Exit levels are 0 success, 1 command failure, 2 usage error, 3 human action
required. With every argument supplied igdev runs in Silent Mode: it never prompts.

Contract: ` + contract.Version + ``

// newRoot builds the command tree for one invocation.
func (a *App) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "igdev",
		Short: "Ignition local-development toolchain",
		Long:  rootLong,
		// The CLI, not cobra, decides what an error looks like.
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rejectUnknownCommand,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
			HiddenDefaultCmd:  true,
		},
	}
	root.PersistentFlags().BoolVar(&a.jsonFlag, "json", false,
		"emit the JSON envelope on stdout (machine mode)")
	root.PersistentFlags().StringArrayVar(&a.configFlag, "config", nil,
		"override one config key for this run, key=value (highest precedence)")

	// Help is documentation, and in machine mode everything on stdout is an
	// envelope: the rendered text becomes data instead of escaping the contract.
	root.SetHelpFunc(a.printHelp)

	root.AddCommand(a.newInitCmd(), a.newSetupCmd(), a.newStatusCmd(), a.newDoctorCmd(), a.newVersionCmd(), a.newGatewayCmd(), a.newBaselineCmd(), a.newModuleCmd(), a.newCatalogCmd())
	root.AddCommand(a.newCheckCmd(), a.newTestCmd(), a.newBuildCmd(), a.newVerifyCmd(), a.newJythonCmd())
	root.AddCommand(a.newCILocalCmd())
	root.AddCommand(a.newAgentCmd())
	completion := newCompletionCmd(root)
	help := newHelpCmd(root)
	root.AddCommand(completion, help)
	root.SetHelpCommand(help)
	return root
}

// rejectUnknownCommand keeps unknown-command handling inside the contract:
// exit 2 with IGDEV_E_USAGE, not a help dump.
func rejectUnknownCommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return contract.UsageFault(fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath()),
		contract.Remediation{Command: "igdev --help", Why: "list the available commands"},
		contract.Remediation{Command: "igdev status --json", Why: "safe first call: report project state as JSON"})
}

// newHelpCmd replaces cobra's help command so an unknown topic is a contract
// usage error instead of a printed apology.
func newHelpCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show help for a command",
		Long:  "Usage:\n  igdev help            show every command\n  igdev help status     show one command with its flags and examples",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return contract.UsageFault(fmt.Sprintf("igdev help takes one <command> argument, got %d", len(args)),
					contract.Remediation{Command: "igdev help", Why: "list the available commands"})
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return root.Help()
			}
			target, remaining, err := root.Find(args)
			if err != nil || target == nil || len(remaining) > 0 {
				return contract.UsageFault(fmt.Sprintf("unknown command %q for %q", strings.Join(args, " "), root.CommandPath()),
					contract.Remediation{Command: "igdev help", Why: "list the available commands"})
			}
			return target.Help()
		},
	}
}
