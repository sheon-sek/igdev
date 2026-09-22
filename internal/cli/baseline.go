package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/baseline"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
)

// baselineData is the `data` member of a successful `baseline set` and
// `baseline status` envelope. The key set and order are frozen by the goldens in
// itest/testdata/golden.
type baselineData struct {
	Staged bool `json:"staged"`
	// Path is the staged file inside the Checkout Setup; empty when none is.
	Path string `json:"path,omitempty"`
	// Bytes is the staged file's size.
	Bytes int64 `json:"bytes,omitempty"`
	// Source is where the staged file was copied from, from its record; empty
	// when the record is absent or no longer describes the file.
	Source string `json:"source,omitempty"`
	// SHA256 is the staged file's digest as recorded at staging time.
	SHA256 string `json:"sha256,omitempty"`
	// StagedAt is when the file was staged, as recorded.
	StagedAt string `json:"staged_at,omitempty"`
	// RestoreArgs is what the next fresh Gateway launch adds to the launcher
	// command; empty when nothing is staged.
	RestoreArgs string `json:"restore_args,omitempty"`
}

// baselineClearData is the `data` member of a successful `baseline clear`
// envelope: the paths the run removed, empty when nothing was staged.
type baselineClearData struct {
	Removed []string `json:"removed"`
}

// baselineOf renders one Baseline state for the caller.
func baselineOf(state baseline.State) baselineData {
	data := baselineData{
		Staged:   state.Staged,
		Path:     state.Path,
		Bytes:    state.Bytes,
		Source:   state.Source,
		SHA256:   state.SHA256,
		StagedAt: state.StagedAt,
	}
	if state.Staged {
		data.RestoreArgs = baseline.Args()
	}
	return data
}

// baselineDir is the Baseline directory of a checkout that passed the Gate.
func baselineDir(found project.Found) string {
	return baseline.Dir(filepath.Join(found.Root, project.StateDir))
}

// requireBaselineGate is the Gate every Baseline verb passes before it touches
// the checkout: discovery, config resolution, contract schema, and a current
// Setup Stamp. A Baseline is checkout-local state, so it needs a materialized
// checkout; asking for one that does not exist yet is what
// IGDEV_E_SETUP_REQUIRED is for.
//
// Consent is deliberately not checked here: staging a backup file is not running
// a Gateway, and Consent is checked by every verb that starts one.
func (a *App) requireBaselineGate() (project.Found, *config.Resolution, error) {
	found, res, err := a.gate()
	if err != nil {
		return found, res, err
	}
	if err := gate.Require(a.gateInput(found)); err != nil {
		return found, res, err
	}
	return found, res, nil
}

func (a *App) newBaselineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Stage and report the Gateway backup this checkout restores from",
		Long: `baseline manages the checkout-local Gateway backup: a .gwbk copied into the
Checkout Setup that the next fresh Gateway launch restores from.

` + "`set`" + ` validates the file (it must exist and be a .gwbk), copies it into
` + "`.igdev/baseline/`" + `, and records what it copied. The copy is the Baseline: the
original may move or disappear afterwards. Staging a second time replaces the
staged file — it is local user state, so there is nothing tracked to diff and no
confirmation to give.

The staged file is kept out of ` + "`.igdev/runtime/`" + `, which ` + "`igdev setup`" + ` owns and
re-renders: re-running setup, including the run that repairs a stale Setup Stamp,
never discards a staged Baseline.

Ignition applies a restore only on a fresh Gateway launch, which is why the wiring
matters to ` + "`igdev gateway reset`" + ` (down --volumes, up, wait): whenever a Baseline
is staged, every Gateway verb hands Compose the restore argument
` + "`-r /restore/restore.gwbk`" + `, and the Compose file ` + "`igdev setup`" + ` rendered mounts the
Baseline directory at ` + "`/restore`" + ` read-only. ` + "`clear`" + ` removes the staged file, so the
next launch restores nothing.

Every Baseline verb passes the Gate (a current Checkout Setup) first; none of them
touches the container engine.`,
		Example: `  igdev baseline set ~/backups/customer.gwbk
  igdev baseline status --json
  igdev baseline clear
  igdev gateway reset`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		a.newBaselineSetCmd(),
		a.newBaselineStatusCmd(),
		a.newBaselineClearCmd(),
	)
	return cmd
}

func (a *App) newBaselineSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <file.gwbk>",
		Short: "Copy a Gateway backup into this checkout as the staged Baseline",
		Long: `set stages an Ignition Gateway backup: the file is validated, copied into the
Checkout Setup's ` + "`.igdev/baseline/`" + `, and recorded with its source, size, and
sha256. The next fresh launch — ` + "`igdev gateway reset`" + ` — restores from the staged
copy, so the file you staged from is free to move or disappear.

The source has to exist and be a .gwbk: a missing file is IGDEV_E_BASELINE_MISSING,
a path that is not a .gwbk is a usage error, and an existing path that cannot be
staged as a file (a directory) is IGDEV_E_BASELINE_INVALID. Nothing is written
unless the source passes all three.

Staging a second time replaces the staged file. The Baseline is checkout-local, so
the replacement is not a tracked-config change and prints no diff.`,
		Example: `  igdev baseline set ~/backups/customer.gwbk
  igdev baseline set /mnt/tickets/1234/dev-baseline.gwbk --json`,
		Args: func(_ *cobra.Command, args []string) error {
			switch len(args) {
			case 0:
				return missingArgument("baseline set", "file", "igdev baseline set /path/to/dev-baseline.gwbk",
					"stage the Gateway backup this checkout restores from")
			case 1:
				return nil
			default:
				return extraArguments("baseline set", 1, args)
			}
		},
		RunE: func(_ *cobra.Command, args []string) error {
			found, res, err := a.requireBaselineGate()
			if err != nil {
				return err
			}
			state, fault := baseline.Stage(args[0], baselineDir(found), time.Now())
			if fault != nil {
				return fault
			}
			data := baselineOf(state)
			a.emit(res, data, func() { a.printBaseline(data) })
			return nil
		},
	}
}

func (a *App) newBaselineStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether a Baseline is staged",
		Long: `status reports the staged Baseline as JSON: whether one is staged, where it is,
how large it is, the source, digest, and timestamp its record carries, and the
restore arguments the next fresh Gateway launch applies. Without a staged file it
reports staged false and nothing else.

The staged file is the state and the record is only a note about it, so a record
that is missing, unreadable, or no longer describes the staged file (a different
size) leaves the Baseline staged without provenance — a hand-placed restore.gwbk
still counts.`,
		Example: `  igdev baseline status
  igdev baseline status --json`,
		Args: rejectArgs("baseline status"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, res, err := a.requireBaselineGate()
			if err != nil {
				return err
			}
			data := baselineOf(baseline.Read(baselineDir(found)))
			a.emit(res, data, func() { a.printBaseline(data) })
			return nil
		},
	}
}

func (a *App) newBaselineClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the staged Baseline",
		Long: `clear removes the staged Baseline and its record, so the next fresh Gateway launch
restores nothing. The Baseline directory itself stays: it is the mount point the
rendered Compose file names, and clearing means "restore nothing", not "unmount".

Clearing when nothing is staged is a successful no-op that removes nothing.`,
		Example: `  igdev baseline clear
  igdev baseline clear --json`,
		Args: rejectArgs("baseline clear"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, res, err := a.requireBaselineGate()
			if err != nil {
				return err
			}
			removed, fault := baseline.Clear(baselineDir(found))
			if fault != nil {
				return fault
			}
			data := baselineClearData{Removed: removed}
			a.emit(res, data, func() { a.printBaselineClear(data) })
			return nil
		},
	}
}

// printBaseline tells a human what is staged and what it will do. The digest is
// the review surface: the staged copy is the one the Gateway restores from.
func (a *App) printBaseline(data baselineData) {
	if !data.Staged {
		fmt.Fprint(a.Stdout, "baseline: none staged\n")
		fmt.Fprint(a.Stdout, "stage one: igdev baseline set <file.gwbk>\n")
		return
	}
	fmt.Fprintf(a.Stdout, "baseline: %s\n", data.Path)
	if data.Source != "" {
		fmt.Fprintf(a.Stdout, "source:   %s\n", data.Source)
		fmt.Fprintf(a.Stdout, "staged:   %s\n", data.StagedAt)
		fmt.Fprintf(a.Stdout, "backup:   %d bytes, sha256 %s\n", data.Bytes, data.SHA256)
	} else {
		fmt.Fprintf(a.Stdout, "backup:   %d bytes (no record: provenance unknown)\n", data.Bytes)
	}
	fmt.Fprintf(a.Stdout, "restore:  %s on the next fresh launch (igdev gateway reset)\n", data.RestoreArgs)
}

func (a *App) printBaselineClear(data baselineClearData) {
	if len(data.Removed) == 0 {
		fmt.Fprint(a.Stdout, "baseline: none staged, nothing removed\n")
		return
	}
	fmt.Fprint(a.Stdout, "cleared:\n")
	for _, path := range data.Removed {
		fmt.Fprintf(a.Stdout, "  %s\n", path)
	}
	fmt.Fprint(a.Stdout, "the next fresh launch restores nothing\n")
}
