package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/instance"
	"github.com/sheon-sek/igdev/internal/ports"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// statusData is the `data` member of a successful `igdev status` envelope. The
// key set and order are frozen by the goldens in itest/testdata/golden.
type statusData struct {
	Initialized bool           `json:"initialized"`
	ProjectRoot string         `json:"project_root"`
	WorkingDir  string         `json:"working_dir"`
	Contract    statusContract `json:"contract"`
	Setup       statusSetup    `json:"setup"`
	Consent     statusConsent  `json:"consent"`
	Config      statusConfig   `json:"config"`
}

// statusContract reports the Project Contract and the Gate's verdict on it.
type statusContract struct {
	Path string `json:"path"`
	// Present is whether a contract file was found at all.
	Present bool `json:"present"`
	// SchemaVersion is the version the file declares; 0 means it declares none.
	SchemaVersion int `json:"schema_version"`
	// SchemaSupported is whether this CLI speaks that schema version. An
	// unsupported contract fails every dependent command closed.
	SchemaSupported bool `json:"schema_supported"`
	// Digest is the Contract Digest: sha256 over the contract bytes.
	Digest string `json:"digest"`
}

// statusSetup reports the Checkout Setup, its Setup Stamp verdict, and the
// Instance identity and ports the record carries. The last two are readable in
// every stamp state: a stale record still names an Instance.
type statusSetup struct {
	Present bool   `json:"present"`
	Path    string `json:"path"`
	// StampState is required (no record), current, or stale.
	StampState string `json:"stamp_state"`
	// InstanceID is the Instance's random UUID; empty before setup.
	InstanceID string `json:"instance_id"`
	// Namespace is the Instance's Docker namespace, igdev-<short-id>; empty
	// before setup.
	Namespace string `json:"namespace"`
	// Ports are the loopback ports allocated to this Instance, or null before
	// setup. Nothing may assume 8088 (ADR 0003).
	Ports *ports.Triplet `json:"ports"`
}

// statusConsent reports the machine-global Consent record (ADR 0004) term by
// term: what this machine has accepted, when, and by which igdev. It is user
// state, so it is reported outside a Project Root too.
type statusConsent struct {
	Path  string       `json:"path"`
	Terms []statusTerm `json:"terms"`
}

// statusTerm is one legal term's acceptance state. accepted_at and cli_version
// are empty until the term is accepted.
type statusTerm struct {
	ID         string `json:"id"`
	Accepted   bool   `json:"accepted"`
	AcceptedAt string `json:"accepted_at"`
	CLIVersion string `json:"cli_version"`
}

type statusConfig struct {
	// TierFiles names the file tiers that actually exist on disk.
	TierFiles map[string]string `json:"tier_files"`
	// Resolved lists every config key with the tier that won precedence.
	Resolved []config.Value `json:"resolved"`
}

func (a *App) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report project, setup, and effective config state",
		Long: `status answers "where am I and what is configured" without changing anything.
It is the safe first call for a human or an agent, and it works before init and setup:
outside a Project Root it reports ok with initialized false.

Initialized means a Project Contract (igdev.toml) was found by searching upward from the
working directory. The contract block reports the declared schema version, whether this
CLI supports it (schema_supported), and the Contract Digest — sha256 over the contract
bytes.

setup reports the Checkout Setup record in .igdev/setup.json and its stamp_state:
required when the record is missing, current when its Setup Stamp matches the contract
digest, schema, and CLI Contract Version, and stale when any of them moved — including
after a hand-edit of the contract. A dependent command refuses to run on required or
stale; ` + "`igdev setup`" + ` re-materializes the checkout. The same block carries the
Instance identity (instance_id, and the Docker namespace igdev-<short-id>) and the ports
allocated to it, so no caller has to guess a port (ADR 0003).

consent reports the machine-global Consent record term by term: the Ignition EULA, the
module licenses, and the module certificates this machine has accepted, with when and
by which igdev. A term that is not accepted is a human action; the command that records
it is named in the remediation of every command that needs it (ADR 0004).

The config block shows every key igdev resolves, its value, and the tier that won:
flag > IGDEV_* environment > .igdev/local.toml > igdev.toml > embedded defaults.`,
		Example: "  igdev status --json\n  igdev status --config ignition.version=8.1.21",
		Args:    rejectArgs("status"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, res, err := a.gate()
			if err != nil {
				return err
			}
			// The Gate's verdict is reported, never enforced here: status has to
			// keep working on the checkout it is describing.
			state, _ := gate.Evaluate(a.gateInput(found))
			data := statusData{
				Initialized: found.InProject(),
				ProjectRoot: found.Root,
				WorkingDir:  found.StartDir,
				Contract: statusContract{
					Path:            found.Contract.Path,
					Present:         found.Contract.Present,
					SchemaVersion:   found.Contract.SchemaVersion,
					SchemaSupported: state.SchemaSupported,
					Digest:          state.Digest,
				},
				Setup:   setupStatus(found, state),
				Consent: consentStatus(),
				Config: statusConfig{
					TierFiles: res.TierFiles,
					Resolved:  res.Values(),
				},
			}
			a.emit(res, data, func() { a.printStatus(found, res, state, data) })
			return nil
		},
	}
}

// setupStatus reports the Checkout Setup record: its stamp state plus the
// Instance identity and ports it carries, which survive a stale stamp.
func setupStatus(found project.Found, state gate.State) statusSetup {
	out := statusSetup{Present: found.Setup, Path: found.SetupPath, StampState: string(state.Stamp)}
	stamp, ok := gate.Decode(found.SetupRaw)
	if !ok {
		return out
	}
	if stamp.InstanceID != "" {
		out.InstanceID = stamp.InstanceID
		out.Namespace = instance.Namespace(stamp.InstanceID)
	}
	if stamp.Ports.Complete() {
		out.Ports = &stamp.Ports
	}
	return out
}

// consentStatus reads the machine-global Consent record. A record igdev cannot
// read reports every term unaccepted rather than failing: status is a report, and
// "nothing is proven accepted" is exactly what an unreadable record means.
func consentStatus() statusConsent {
	path := consent.Path(xdg.Resolve().Config)
	record, _ := consent.Load(path)
	out := statusConsent{Path: path, Terms: make([]statusTerm, 0, len(consent.Terms()))}
	for _, term := range consent.Terms() {
		entry := statusTerm{ID: term.ID}
		if accepted, ok := record.Accepted(term); ok {
			entry.Accepted = true
			entry.AcceptedAt = accepted.AcceptedAt
			entry.CLIVersion = accepted.CLIVersion
		}
		out.Terms = append(out.Terms, entry)
	}
	return out
}

func (a *App) printStatus(found project.Found, res *config.Resolution, state gate.State, data statusData) {
	out := a.Stdout
	if found.InProject() {
		fmt.Fprint(out, "project:   initialized\n")
		fmt.Fprintf(out, "root:      %s\n", found.Root)
		fmt.Fprintf(out, "contract:  %s (schema %d)\n", found.Contract.Path, found.Contract.SchemaVersion)
		fmt.Fprintf(out, "digest:    %s\n", state.Digest)
		fmt.Fprintf(out, "schema:    %s\n", supportedWord(state.SchemaSupported))
		if found.Setup {
			fmt.Fprintf(out, "setup:     %s\n", found.SetupPath)
		} else {
			fmt.Fprintf(out, "setup:     missing (%s)\n", found.SetupPath)
		}
		fmt.Fprintf(out, "stamp:     %s\n", state.Stamp)
		if data.Setup.InstanceID != "" {
			fmt.Fprintf(out, "instance:  %s (%s)\n", data.Setup.InstanceID, data.Setup.Namespace)
		}
		if data.Setup.Ports != nil {
			fmt.Fprintf(out, "ports:     http %d, https %d, debug %d\n",
				data.Setup.Ports.HTTP, data.Setup.Ports.HTTPS, data.Setup.Ports.Debug)
		}
	} else {
		fmt.Fprint(out, "project:   not initialized\n")
		fmt.Fprintf(out, "root:      none (no %s found at or above %s)\n", project.ContractFile, found.StartDir)
		fmt.Fprint(out, "contract:  missing\n")
		fmt.Fprintf(out, "setup:     missing (%s)\n", found.SetupPath)
	}
	fmt.Fprintf(out, "consent:   %s\n", data.Consent.Path)
	for _, term := range data.Consent.Terms {
		if term.Accepted {
			fmt.Fprintf(out, "  %-18s accepted %s (%s)\n", term.ID, term.AcceptedAt, term.CLIVersion)
			continue
		}
		fmt.Fprintf(out, "  %-18s not accepted\n", term.ID)
	}
	fmt.Fprint(out, "config:\n")
	table := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	for _, v := range res.Values() {
		fmt.Fprintf(table, "  %s\t%v\t[%s]\n", v.Key, v.Value, v.Source)
	}
	table.Flush()
}

func supportedWord(supported bool) string {
	if supported {
		return "supported"
	}
	return "unsupported"
}

// rejectArgs freezes "this command takes no positional arguments".
func rejectArgs(name string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return extraArguments(name, 0, args)
		}
		return nil
	}
}
