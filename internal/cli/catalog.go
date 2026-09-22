package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/semver"
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

` + "`catalog import-openapi`" + ` writes the REST plane of this repository's tracked
overlay from a Gateway's OpenAPI document; this command only reports.`,
		Example: `  igdev catalog status
  igdev catalog status --json
  igdev catalog status --config ignition.version=8.1.21
  igdev catalog import-openapi openapi.json`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newCatalogStatusCmd(), a.newCatalogImportCmd())
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

// catalogImportData is the `data` member of a successful
// `igdev catalog import-openapi` envelope. The key set and order are frozen by the
// goldens in itest/testdata/golden.
type catalogImportData struct {
	// IgnitionVersion is the version the imported rows are resolved against.
	IgnitionVersion string              `json:"ignition_version"`
	Source          catalogImportSource `json:"source"`
	Output          catalogImportOutput `json:"output"`
	Operations      int                 `json:"operations"`
	Written         int                 `json:"written"`
	Added           int                 `json:"added"`
	Preserved       int                 `json:"preserved"`
	Removed         int                 `json:"removed"`
	Redundant       int                 `json:"redundant"`
}

// catalogImportSource is the OpenAPI document the run read: the path as the caller
// typed it and the sha256 of its bytes, which is the digest the overlay header
// records.
type catalogImportSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// catalogImportOutput is the overlay file the run wrote, or would have written
// byte-identically.
type catalogImportOutput struct {
	// Path is the tracked, repository-relative overlay file.
	Path string `json:"path"`
	// Action is created, updated, or unchanged: a re-import of an unchanged
	// document is not a change.
	Action string `json:"action"`
	// Digest is the sha256 of the file's bytes after the run.
	Digest string `json:"digest"`
	// Declared reports whether the Project Contract's [catalog].overlay_paths
	// names the file. A file nothing declares is never resolved.
	Declared bool `json:"declared"`
	// Diff is the unified diff of a write, empty when nothing changed.
	Diff string `json:"diff"`
}

func (a *App) newCatalogImportCmd() *cobra.Command {
	var (
		ignitionVersion string
		output          string
		prune           bool
	)
	cmd := &cobra.Command{
		Use:   "import-openapi <openapi.json>",
		Short: "Write the tracked REST overlay from a Gateway OpenAPI document",
		Long: `import-openapi reads a Gateway ` + "`/openapi.json`" + ` snapshot and writes the REST plane
of this repository's tracked Project Overlay: one row per operation, mapping the
path template to its owner kind and required modules with the same rules the legacy
` + "`scripts/generate-rest-catalog.py`" + ` used (ADR 0005).

The REST plane is generated, so it is replaced wholesale inside a block the file
marks as generated; the native function and capability rule planes are
hand-authored, because an OpenAPI document knows nothing about them, and are left
untouched. Each row the Core Catalog already carries with the same owner is not
copied into the overlay — a duplicate would be refused as a conflict on the next
read — so importing the full document of an Ignition version this binary knows is a
no-op for the rows that version's core already has, and the overlay grows by what
the core does not know.

A row that would shadow a Core Catalog row with a different owner is
IGDEV_E_OVERLAY_CONFLICT and nothing is written: an overlay may add knowledge and
may not silently redefine it. The same applies to a hand-authored REST row in the
file, so a conflicting overlay is refused rather than half-rewritten.

Re-importing an unchanged document writes nothing and reports ` + "`unchanged`" + `; a
document that changed updates the overlay and the digest the header records. A row a
previous import wrote that the document no longer declares is kept, because a
Gateway that stopped serving an endpoint is not proof the project stopped using it;
` + "`--prune`" + ` is the opt-in that drops those rows.

The file written is ` + "`--output`" + `, or the first ` + "`[catalog].overlay_paths`" + ` entry, or
` + "`catalog/rest-overlay.tsv`" + `. It has to be repository-relative and inside the Project
Root, and the run warns on stderr when the contract does not declare it: an overlay
nothing declares is never resolved. The Ignition version is ` + "`--ignition-version`" + `, or
the resolved ` + "`ignition.version`" + `; a version this binary carries no Core Catalog for
cannot be checked against anything, and fails closed with
IGDEV_E_CATALOG_VERSION_MISSING.

The write is atomic and prints a unified diff, exactly like a Contract write.`,
		Example: `  igdev catalog import-openapi openapi.json
  igdev catalog import-openapi openapi.json --json
  igdev catalog import-openapi openapi.json --prune
  igdev catalog import-openapi /tmp/openapi.json --ignition-version 8.3.8 --output catalog/rest-overlay.tsv`,
		Args: func(_ *cobra.Command, args []string) error {
			switch len(args) {
			case 0:
				return missingArgument("catalog import-openapi", "openapi.json",
					"igdev catalog import-openapi openapi.json",
					"name the Gateway /openapi.json snapshot to import")
			case 1:
				return nil
			default:
				return extraArguments("catalog import-openapi", 1, args)
			}
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return a.importOpenAPI(args[0], ignitionVersion, output, prune)
		},
	}
	cmd.Flags().StringVar(&ignitionVersion, "ignition-version", "",
		"Ignition version the snapshot represents (default: the resolved ignition.version)")
	cmd.Flags().StringVar(&output, "output", "",
		"overlay file to write (default: the first [catalog].overlay_paths entry, else "+catalog.DefaultOverlayOutput+")")
	cmd.Flags().BoolVar(&prune, "prune", false,
		"drop previously imported rows the document no longer declares")
	return cmd
}

// importOpenAPI is the whole of the verb: resolve where the overlay is and which
// version it is for, generate the REST plane, and write it as a tracked file.
func (a *App) importOpenAPI(source, flagVersion, flagOutput string, prune bool) error {
	found, res, err := a.gate()
	if err != nil {
		return err
	}
	if fault := gate.RequireProject(a.gateInput(found)); fault != nil {
		return fault
	}
	_, doc, fault := gate.ContractOnly(a.gateInput(found))
	if fault != nil {
		return fault
	}

	version := res.String("ignition.version")
	if value := strings.TrimSpace(flagVersion); value != "" {
		if _, err := semver.Parse(value); err != nil {
			return contract.UsageFault(
				fmt.Sprintf("igdev catalog import-openapi: --ignition-version %q is not an Ignition version", value),
				contract.Remediation{
					Command: "igdev catalog import-openapi " + source + " --ignition-version " + catalog.Target,
					Why:     "import against the version this igdev carries a Core Catalog for",
				})
		}
		version = value
	}

	target := strings.TrimSpace(flagOutput)
	if target == "" {
		if len(doc.Catalog.OverlayPaths) > 0 {
			target = doc.Catalog.OverlayPaths[0]
		} else {
			target = catalog.DefaultOverlayOutput
		}
	}
	full, fault := overlayWritePath(found.Root, target)
	if fault != nil {
		return fault
	}

	document := source
	if !filepath.IsAbs(document) {
		document = filepath.Join(a.Dir, document)
	}
	operations, raw, fault := catalog.ParseOpenAPIFile(document, source)
	if fault != nil {
		return fault
	}

	existing, readErr := os.ReadFile(full)
	switch {
	case readErr == nil:
	case os.IsNotExist(readErr):
		existing = nil
	default:
		return writeFault(full, readErr)
	}

	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	imported, fault := catalog.ImportOpenAPI(version, existing, operations, digest, prune, target)
	if fault != nil {
		return fault
	}

	file, err := writeTracked(full, target, existing, imported.Content, 0o644)
	if err != nil {
		return err
	}
	declared := declaresOverlay(doc.Catalog.OverlayPaths, target)
	if !declared {
		fmt.Fprintf(a.Stderr,
			"[igdev] WARNING: %s is not declared in [catalog].overlay_paths; add it so this repository resolves the overlay\n", target)
	}

	data := catalogImportData{
		IgnitionVersion: version,
		Source:          catalogImportSource{Path: source, SHA256: digest},
		Output: catalogImportOutput{
			Path:     target,
			Action:   file.Action,
			Digest:   project.Digest(imported.Content),
			Declared: declared,
			Diff:     file.Diff,
		},
		Operations: imported.Operations,
		Written:    imported.Written,
		Added:      imported.Added,
		Preserved:  imported.Preserved,
		Removed:    imported.Removed,
		Redundant:  imported.Redundant,
	}
	a.emit(res, data, func() { a.printCatalogImport(data) })
	return nil
}

// overlayWritePath resolves the overlay file an import writes. The path is a
// tracked file, so it has to stay inside the Project Root: a write outside the
// repository is not a Project Overlay and is refused before anything is read.
func overlayWritePath(root, target string) (string, *contract.Fault) {
	if filepath.IsAbs(target) {
		return "", contract.UsageFault(
			fmt.Sprintf("--output %s is an absolute path; a Project Overlay is a tracked file, so the path has to be repository-relative", target),
			contract.Remediation{
				Command: "igdev catalog import-openapi openapi.json",
				Why:     "write the declared overlay file instead",
			})
	}
	cleaned := filepath.Clean(target)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", contract.UsageFault(
			fmt.Sprintf("--output %s resolves outside the Project Root", target),
			contract.Remediation{
				Command: "igdev catalog import-openapi openapi.json",
				Why:     "write the declared overlay file instead",
			})
	}
	return filepath.Join(root, cleaned), nil
}

// declaresOverlay reports whether the contract's [catalog].overlay_paths names the
// file, comparing the way the overlay reader resolves it.
func declaresOverlay(paths []string, target string) bool {
	for _, declared := range paths {
		if filepath.Clean(declared) == filepath.Clean(target) {
			return true
		}
	}
	return false
}

// printCatalogImport reports the write the way a tracked-file change is reported
// everywhere else: the diff on stderr, one summary per fact on stdout.
func (a *App) printCatalogImport(data catalogImportData) {
	if data.Output.Diff != "" {
		fmt.Fprint(a.Stderr, data.Output.Diff)
	}
	fmt.Fprintf(a.Stdout, "source:  %s (%s)\n", data.Source.Path, data.Source.SHA256)
	fmt.Fprintf(a.Stdout, "overlay: %s (%s, %d of %d operations, digest %s)\n",
		data.Output.Path, data.Output.Action, data.Written, data.Operations, data.Output.Digest)
	fmt.Fprintf(a.Stdout, "rows:    added %d, preserved %d, removed %d, redundant %d\n",
		data.Added, data.Preserved, data.Removed, data.Redundant)
}
