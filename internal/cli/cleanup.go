package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/cleanup"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// The scopes `igdev cleanup` reports.
const (
	cleanupScopeCheckout = "checkout"
	cleanupScopeMachine  = "machine"
)

// cleanupData is the `igdev cleanup` result.
type cleanupData struct {
	Scope     string          `json:"scope"`
	Deinit    bool            `json:"deinit"`
	Uninstall bool            `json:"uninstall"`
	DryRun    bool            `json:"dry_run"`
	Root      string          `json:"project_root"`
	Namespace string          `json:"namespace"`
	Items     []*cleanup.Item `json:"items"`
	Removed   int             `json:"removed"`
	Pending   int             `json:"would_remove"`
	Kept      int             `json:"kept"`
	Failed    int             `json:"failed"`
}

func (a *App) newCleanupCmd() *cobra.Command {
	var deinit, machine, uninstall, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove everything igdev created, for this checkout or the whole machine",
		Long: `cleanup removes what igdev created once a project is finished. With no flag it
removes this checkout's Instance: its Gateway and trial-keeper containers, its data
volume and network, the image igdev built for it, and the Checkout Setup (.igdev/).
The Gateway's projects and data go with it, as with ` + "`gateway reset`" + `.

` + "`--deinit`" + ` also removes what igdev wrote into the repository: igdev.toml, the
managed block in AGENTS.md, and a repository-scope Agent Skill. The changes stay in the
working tree for you to review and commit; .gitignore is left as it is.

` + "`--machine`" + ` removes every igdev Instance on this machine, including ones whose
checkout is gone, every official Ignition image no container uses, igdev's cache,
state and config directories (the Consent record among them), and the global Agent
Skills. ` + "`--uninstall`" + ` implies ` + "`--machine`" + ` and finally removes the igdev binary
and the PATH entry install.sh added to the login profile.

Things igdev uses but does not own stay and are listed as kept: the Docker build cache,
act's images and cache, files IGDEV_CONSENT_FILE names, and tracked content the
contract points at (overlays, the seed). Every run is idempotent; ` + "`--dry-run`" + ` lists
the plan and changes nothing. ` + "`--machine`" + ` and ` + "`--uninstall`" + ` reach beyond this
checkout, so they need ` + "`--yes`" + `: in a terminal cleanup asks once instead.`,
		Example: `  igdev cleanup
  igdev cleanup --dry-run --json
  igdev cleanup --deinit
  igdev cleanup --machine --yes
  igdev cleanup --uninstall --yes`,
		Args: rejectArgs("cleanup"),
		RunE: func(_ *cobra.Command, _ []string) error {
			if uninstall {
				machine = true
			}
			found, err := project.Discover(a.Dir)
			if err != nil {
				return err
			}
			res, err := a.resolvedForCleanup(found)
			if err != nil {
				return err
			}
			opts, data := a.cleanupOptions(found, deinit, machine, uninstall)
			data.DryRun = dryRun
			plan, fault := cleanup.Build(opts)
			if fault != nil {
				return fault
			}
			if (machine || uninstall) && !yes && !dryRun && plan.Pending() > 0 {
				confirmed, fault := a.confirmCleanup(res, plan, data)
				if fault != nil {
					return fault
				}
				if !confirmed {
					fmt.Fprintln(a.Stderr, "[igdev] cleanup: nothing removed")
					return nil
				}
			}
			if !dryRun {
				plan.Execute()
			}
			data = summarize(data, plan)
			if data.Failed > 0 {
				return contract.NewFault(contract.CodeCleanupPartial, contract.ExitFailure,
					fmt.Sprintf("cleanup removed %d items but %d failed; items[] carries each reason", data.Removed, data.Failed)).
					WithData(data).
					WithRemediation(contract.Remediation{Command: cleanupCommand(deinit, machine, uninstall, true),
						Why: "fix what the failed items name, then run cleanup again: it is idempotent"})
			}
			// No update notice: it caches its answer under ~/.cache/igdev, which
			// would re-create the directory a machine cleanup just removed.
			if res.IsJSON() {
				a.writeEnvelope(contract.Success(data))
			} else {
				a.printCleanup(data)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deinit, "deinit", false,
		"also remove igdev.toml, the AGENTS.md block, and the repository-scope Agent Skill")
	cmd.Flags().BoolVar(&machine, "machine", false,
		"clean every igdev Instance and igdev's machine-level files, not only this checkout")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false,
		"also remove the igdev binary and install.sh's PATH entry (implies --machine)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed and change nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm --machine or --uninstall without asking")
	return cmd
}

// resolvedForCleanup resolves config for cleanup without letting a broken tier
// stop it: cleanup is how a person gets rid of a contract or local config igdev
// can no longer read, so such a tier is dropped and the defaults apply.
func (a *App) resolvedForCleanup(found project.Found) (*config.Resolution, error) {
	in := a.configInput(found)
	if res, err := config.Resolve(in); err == nil {
		return res, nil
	}
	in.ContractTOML, in.ContractPath, in.LocalTOML, in.LocalPath = nil, "", nil, ""
	return config.Resolve(in)
}

// cleanupOptions finds the checkout cleanup acts on. It deliberately bypasses the
// Gate's Setup refresh: rebuilding .igdev/ in order to delete it would be absurd,
// and a missing or stale setup is exactly the state a cleanup may start from. A
// directory holding only a `.igdev/` (its igdev.toml already gone) is still a
// checkout to clean.
func (a *App) cleanupOptions(found project.Found, deinit, machine, uninstall bool) (cleanup.Options, cleanupData) {
	root, raw := found.Root, found.SetupRaw
	if root == "" {
		if record, err := os.ReadFile(filepath.Join(found.StartDir, project.StateDir, project.SetupRecord)); err == nil {
			root, raw = found.StartDir, record
		}
	}
	namespace := ""
	if stamp, ok := gate.Decode(raw); ok {
		namespace = stamp.Namespace()
	}
	executable := ""
	if uninstall {
		if exe, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			executable = exe
		}
	}
	scope := cleanupScopeCheckout
	if machine {
		scope = cleanupScopeMachine
	}
	opts := cleanup.Options{
		Root: root, Namespace: namespace, ContractTOML: found.ContractTOML,
		Deinit: deinit, Machine: machine, Uninstall: uninstall,
		Home: xdg.Home(), Dirs: xdg.Resolve(), Executable: executable,
		Environ: config.EnvironMap(a.Environ),
		Agents:  cleanup.AgentsBlock{File: agentsFile, Start: agentsMarkStart, End: agentsMarkEnd},
		Docker:  cleanup.Exec,
	}
	return opts, cleanupData{Scope: scope, Deinit: deinit, Uninstall: uninstall, Root: root, Namespace: namespace}
}

// confirmCleanup is the one confirmation cleanup asks for, and only for the
// scopes that reach beyond this checkout. A person at a terminal answers y/N
// once; anything else gets the plan back with the exact command to confirm it.
func (a *App) confirmCleanup(res *config.Resolution, plan cleanup.Plan, data cleanupData) (bool, *contract.Fault) {
	command := cleanupCommand(data.Deinit, true, data.Uninstall, true)
	if res.IsJSON() || !a.stdinTerminal() {
		data.DryRun = true
		data = summarize(data, plan)
		return false, contract.UsageFault(
			fmt.Sprintf("cleanup --%s reaches beyond this checkout and would remove %d items: confirm with --yes",
				map[bool]string{true: "uninstall", false: "machine"}[data.Uninstall], data.Pending),
			contract.Remediation{Command: command, Why: "remove the items data.items lists"},
			contract.Remediation{Command: "igdev cleanup", Why: "clean only this checkout, which needs no confirmation"}).
			WithData(data)
	}
	data.DryRun = true
	a.printCleanup(summarize(data, plan))
	fmt.Fprintf(a.Stderr, "[igdev] remove these %d items? [y/N] ", plan.Pending())
	answer, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// cleanupCommand renders the invocation for a scope.
func cleanupCommand(deinit, machine, uninstall, yes bool) string {
	parts := []string{"igdev cleanup"}
	if deinit {
		parts = append(parts, "--deinit")
	}
	switch {
	case uninstall:
		parts = append(parts, "--uninstall")
	case machine:
		parts = append(parts, "--machine")
	}
	if yes && (machine || uninstall) {
		parts = append(parts, "--yes")
	}
	return strings.Join(parts, " ")
}

func summarize(data cleanupData, plan cleanup.Plan) cleanupData {
	data.Items = plan.Items
	if data.Items == nil {
		data.Items = []*cleanup.Item{}
	}
	data.Removed = plan.Count(cleanup.Removed)
	data.Pending = plan.Count(cleanup.WouldRemove)
	data.Kept = plan.Count(cleanup.Kept)
	data.Failed = plan.Count(cleanup.Failed)
	return data
}

func (a *App) printCleanup(data cleanupData) {
	for _, item := range data.Items {
		if item.Diff != "" && item.Action != cleanup.Kept {
			fmt.Fprint(a.Stderr, item.Diff)
		}
	}
	if len(data.Items) == 0 {
		fmt.Fprintf(a.Stdout, "cleanup:  nothing igdev created is left (%s scope)\n", data.Scope)
		return
	}
	for _, item := range data.Items {
		line := fmt.Sprintf("%-13s %-15s %s", item.Action, item.Kind, item.Ref)
		if item.Reason != "" {
			line += "  (" + item.Reason + ")"
		}
		fmt.Fprintln(a.Stdout, line)
	}
	if data.DryRun {
		fmt.Fprintf(a.Stdout, "cleanup:  %d would be removed, %d kept (dry run, %s scope)\n", data.Pending, data.Kept, data.Scope)
		return
	}
	fmt.Fprintf(a.Stdout, "cleanup:  %d removed, %d kept, %d failed (%s scope)\n", data.Removed, data.Kept, data.Failed, data.Scope)
}
