package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/project"
)

// catalogStatusData is the `data` member of a successful `igdev catalog status`
// envelope. The key set and order are frozen by the goldens in
// itest/testdata/golden.
type catalogStatusData struct {
	// IgnitionVersion is the version the Effective Catalog resolves for, from
	// the config precedence chain.
	IgnitionVersion string `json:"ignition_version"`
	// Core is the embedded layer; Overlay is this repository's tracked layer.
	Core    catalogLayerData `json:"core"`
	Overlay catalogLayerData `json:"overlay"`
	// Effective is the layer-by-layer sum: what preflight consults.
	Effective catalog.Counts `json:"effective"`
}

// catalogLayerData is one layer of the Effective Catalog. The digest is the
// layer's integrity: sha256 over the embedded planes, or over the declared
// overlay files in order. The counts are reported for a reader and are never
// asserted by tests — the digest is what proves the data (ADR 0005).
type catalogLayerData struct {
	// Version is the Ignition version an embedded layer carries; empty for a
	// project layer.
	Version string `json:"version,omitempty"`
	// Source is how the layer was obtained: embedded or project.
	Source string `json:"source"`
	Digest string `json:"digest"`
	// Paths are the declared overlay files, in resolution order.
	Paths  []string       `json:"paths,omitempty"`
	Counts catalog.Counts `json:"counts"`
}

func (a *App) newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Inspect the capability knowledge the CLI resolves against",
		Long: `catalog reports the Effective Catalog: the Core Catalog embedded in this binary,
keyed by Ignition version, plus this repository's tracked Project Overlay (ADR
0005).

The Project Overlay is a set of TSV files the contract declares in
` + "`[catalog].overlay_paths`" + `, each with the column shape of the Core Catalog plane it
extends. It may add native functions, capability rules, and REST operations the
core does not carry — that is how a private or third-party module's endpoints
become checkable without a new igdev release. It may not shadow a core row:
a row whose key the core already holds is IGDEV_E_OVERLAY_CONFLICT, naming both
rows, because a silently redefined core row is no longer reviewable.

Outside a Project Root the answer is the Core Catalog alone, which makes this the
command that says which Ignition versions this binary knows.

` + "`catalog import-openapi`" + ` (ticket 13) writes a tracked overlay from a Gateway's
OpenAPI document; this command only reports.`,
		Example: `  igdev catalog status
  igdev catalog status --json
  igdev catalog status --config ignition.version=8.1.21`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newCatalogStatusCmd())
	return cmd
}

func (a *App) newCatalogStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the Core Catalog and Project Overlay layers with digests",
		Long: `status reports both layers of the Effective Catalog: where each came from, its
sha256 digest, and its row counts.

The digest is the layer's identity and the review surface. The core digest covers
the embedded plane files in a frozen order; the overlay digest covers the declared
overlay files, in declaration order, so any edit to a tracked overlay changes it.
Two checkouts that resolve the same capabilities therefore report the same
digests.

Row counts are reported because a human wants to see the shape of the knowledge,
never asserted: a count is a maintenance chore, a digest is a proof.

The Ignition version resolved comes from the config precedence chain, so
--config ignition.version=8.1.21 asks about a version this binary carries no
catalog for and fails with IGDEV_E_CATALOG_VERSION_MISSING.`,
		Example: `  igdev catalog status
  igdev catalog status --json`,
		Args: rejectArgs("catalog status"),
		RunE: func(_ *cobra.Command, _ []string) error {
			found, res, _, eff, err := a.catalogContext()
			if err != nil {
				return err
			}
			data := catalogStatusData{
				IgnitionVersion: eff.Version(),
				Core: catalogLayerData{
					Version: eff.Version(),
					Source:  "embedded",
					Digest:  eff.CoreDigest(),
					Counts:  eff.CoreCounts(),
				},
				Overlay: catalogLayerData{
					Source: "project",
					Digest: eff.OverlayDigest(),
					Paths:  eff.OverlayPaths(),
					Counts: eff.OverlayCounts(),
				},
				Effective: eff.Counts(),
			}
			a.emit(res, data, func() { a.printCatalogStatus(found, data) })
			return nil
		},
	}
}

// printCatalogStatus reports the layers as prose, one line each: the digest is
// long, and it is the point of the command.
func (a *App) printCatalogStatus(found project.Found, data catalogStatusData) {
	fmt.Fprintf(a.Stdout, "ignition:  %s\n", data.IgnitionVersion)
	if found.InProject() {
		fmt.Fprintf(a.Stdout, "project:   %s\n", found.Root)
	} else {
		fmt.Fprintf(a.Stdout, "project:   none (Core Catalog only)\n")
	}
	fmt.Fprintf(a.Stdout, "core:      embedded %s, %s, %s\n",
		data.Core.Version, data.Core.Digest, describeCounts(data.Core.Counts))
	switch {
	case len(data.Overlay.Paths) == 0:
		fmt.Fprintf(a.Stdout, "overlay:   none declared, %s, %s\n", data.Overlay.Digest, describeCounts(data.Overlay.Counts))
	default:
		fmt.Fprintf(a.Stdout, "overlay:   %d file(s), %s, %s\n",
			len(data.Overlay.Paths), data.Overlay.Digest, describeCounts(data.Overlay.Counts))
		for _, path := range data.Overlay.Paths {
			fmt.Fprintf(a.Stdout, "  %s\n", path)
		}
	}
	fmt.Fprintf(a.Stdout, "effective: %s\n", describeCounts(data.Effective))
}

// describeCounts renders one layer's row counts.
func describeCounts(counts catalog.Counts) string {
	return fmt.Sprintf("%d built-in modules / %d native functions / %d capability rules / %d REST operations",
		counts.BuiltinModules, counts.NativeFunctions, counts.CapabilityRules, counts.RestOperations)
}
