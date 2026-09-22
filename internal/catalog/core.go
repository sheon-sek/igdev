package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// assets holds the Core Catalog data, one directory per Ignition version. The
// files carry the four TSV plane shapes; they are copied into the binary at build
// time so it reads no repository file at runtime (ADR 0005).
//
//go:embed assets
var assets embed.FS

// Plane names the four data planes, in the frozen order the core digest covers
// them. The order is part of the digest: reordering the planes changes it.
type Plane string

const (
	PlaneBuiltinModules Plane = "builtin-modules.tsv"
	PlaneFunctions      Plane = "native-system-functions.tsv"
	PlaneRules          Plane = "capability-modules.tsv"
	PlaneREST           Plane = "rest-endpoints.tsv"
)

// Planes is the frozen plane order.
var Planes = []Plane{PlaneBuiltinModules, PlaneFunctions, PlaneRules, PlaneREST}

// coreAsset is one plane's embedded file name inside a version directory.
func coreAsset(version string, plane Plane) string {
	return path.Join("assets", version, string(plane))
}

// Versions lists the Ignition versions this binary carries a Core Catalog for,
// in name order.
func Versions() []string {
	entries, err := assets.ReadDir("assets")
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := assets.ReadFile(coreAsset(entry.Name(), PlaneBuiltinModules)); err != nil {
			continue
		}
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// Core loads the embedded Core Catalog for one Ignition version. A version this
// binary carries no data for is IGDEV_E_CATALOG_VERSION_MISSING: knowledge fails
// closed rather than borrowing another version's rows.
func Core(version string) (*Catalog, *contract.Fault) {
	planed := map[Plane][]tsvRow{}
	for _, plane := range Planes {
		raw, err := assets.ReadFile(coreAsset(version, plane))
		if err != nil {
			return nil, missingVersionFault(version)
		}
		planed[plane] = dataRows(raw)
	}
	return parsePlanes(version, planed, LayerCore)
}

// CoreDigest is the sha256 of the embedded data for one version: every plane in
// the frozen order, each preceded by its file name and a NUL so the digest names
// exactly the bytes it covers. Tests assert this against constants; row counts
// are reported elsewhere and never asserted (ADR 0005).
func CoreDigest(version string) (string, *contract.Fault) {
	rows := make([]digestRow, 0, len(Planes))
	for _, plane := range Planes {
		raw, err := assets.ReadFile(coreAsset(version, plane))
		if err != nil {
			return "", missingVersionFault(version)
		}
		rows = append(rows, digestRow{name: string(plane), data: raw})
	}
	return digest(rows), nil
}

// digestRow is one named byte stream inside a layer digest.
type digestRow struct {
	name string
	data []byte
}

// digest hashes named byte streams in order, with NUL separators that make the
// boundaries part of the digest.
func digest(rows []digestRow) string {
	sum := sha256.New()
	for _, row := range rows {
		sum.Write([]byte(row.name))
		sum.Write([]byte{0})
		sum.Write(row.data)
		sum.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

// missingVersionFault is the clean answer for an Ignition version with no
// embedded Core Catalog.
func missingVersionFault(version string) *contract.Fault {
	return contract.NewFault(contract.CodeCatalogVersionMissing, contract.ExitFailure,
		fmt.Sprintf("no Core Catalog for Ignition %s; this igdev carries %s",
			version, strings.Join(Versions(), ", "))).
		WithRemediation(
			contract.Remediation{
				Command: "igdev catalog status --json",
				Why:     "list the catalog layers and versions this binary carries",
			},
			contract.Remediation{
				Command: "igdev status --config ignition.version=" + Target,
				Why:     "ask for the Ignition version this binary knows",
			})
}

// tsvRow is one meaningful data row of a TSV plane, with the 1-based line number
// it came from so a malformed row can be named.
type tsvRow struct {
	line  int
	cells []string
}

// dataRows yields the meaningful rows of a TSV plane: comments and blank lines
// dropped, a trailing carriage return removed, and a trailing empty notes column
// preserved.
func dataRows(raw []byte) []tsvRow {
	var out []tsvRow
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, tsvRow{line: i + 1, cells: strings.Split(line, "\t")})
	}
	return out
}

// parsePlanes interprets the four plane row sets of one layer. A row that cannot
// mean anything is a fault, not a silently dropped line.
func parsePlanes(version string, planed map[Plane][]tsvRow, layer Layer) (*Catalog, *contract.Fault) {
	out := &Catalog{Version: version}
	var fault *contract.Fault
	if out.BuiltinModules, fault = parseBuiltinModules(planed[PlaneBuiltinModules], layer); fault != nil {
		return nil, fault
	}
	if out.Functions, fault = parseFunctions(planed[PlaneFunctions], layer); fault != nil {
		return nil, fault
	}
	if out.Rules, fault = parseRules(planed[PlaneRules], layer); fault != nil {
		return nil, fault
	}
	if out.REST, fault = parseREST(planed[PlaneREST], layer); fault != nil {
		return nil, fault
	}
	return out, nil
}

// planeFault attributes a malformed row to its layer: embedded data that does
// not parse is a build defect, an overlay row that does not parse is the
// project's to fix.
func planeFault(layer Layer, plane Plane, line int, format string, args ...any) *contract.Fault {
	message := fmt.Sprintf("%s %s line %d: %s", layer, plane, line, fmt.Sprintf(format, args...))
	if layer == LayerOverlay {
		return contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure, message)
	}
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure, message)
}

// parseModuleIDs splits a comma-separated module column. "-" and "" mean none.
func parseModuleIDs(cell string) []string {
	if strings.TrimSpace(cell) == "" || cell == "-" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(cell, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBuiltinModules(rows []tsvRow, layer Layer) ([]BuiltinModule, *contract.Fault) {
	var out []BuiltinModule
	for _, row := range rows {
		if len(row.cells) < 2 {
			return nil, planeFault(layer, PlaneBuiltinModules, row.line,
				"expected <module_id><TAB><module_file>, got %d column(s)", len(row.cells))
		}
		id := strings.TrimSpace(row.cells[0])
		if id == "" {
			return nil, planeFault(layer, PlaneBuiltinModules, row.line, "empty module id")
		}
		out = append(out, BuiltinModule{ID: id, Artifact: strings.TrimSpace(row.cells[1])})
	}
	return out, nil
}

func parseFunctions(rows []tsvRow, layer Layer) ([]NativeFunction, *contract.Fault) {
	var out []NativeFunction
	for _, row := range rows {
		if len(row.cells) < 3 {
			return nil, planeFault(layer, PlaneFunctions, row.line,
				"expected <function><TAB><classification><TAB><module_ids>, got %d column(s)", len(row.cells))
		}
		fn := strings.TrimSpace(row.cells[0])
		class := Classification(strings.TrimSpace(row.cells[1]))
		modules := parseModuleIDs(row.cells[2])
		switch class {
		case ClassPlatform:
			if len(modules) > 0 {
				return nil, planeFault(layer, PlaneFunctions, row.line,
					"%s is platform but names modules %s", fn, strings.Join(modules, ","))
			}
		case ClassModule, ClassConditional:
			if len(modules) == 0 {
				return nil, planeFault(layer, PlaneFunctions, row.line, "%s is %s but names no module", fn, class)
			}
		default:
			return nil, planeFault(layer, PlaneFunctions, row.line,
				"%s has classification %q, want platform, module, or conditional", fn, class)
		}
		if !functionName(fn) {
			return nil, planeFault(layer, PlaneFunctions, row.line, "%q is not a system.* function name", fn)
		}
		note := ""
		if len(row.cells) > 3 {
			note = strings.TrimSpace(strings.Join(row.cells[3:], "\t"))
		}
		out = append(out, NativeFunction{Function: fn, Class: class, Modules: modules, Note: note})
	}
	return out, nil
}

func parseRules(rows []tsvRow, layer Layer) ([]CapabilityRule, *contract.Fault) {
	var out []CapabilityRule
	for _, row := range rows {
		if len(row.cells) < 3 {
			return nil, planeFault(layer, PlaneRules, row.line,
				"expected <kind><TAB><pattern><TAB><module_ids>, got %d column(s)", len(row.cells))
		}
		kind := RuleKind(strings.TrimSpace(row.cells[0]))
		if kind != RuleExact && kind != RulePrefix {
			return nil, planeFault(layer, PlaneRules, row.line, "kind %q is neither exact nor prefix", kind)
		}
		pattern := strings.TrimSpace(row.cells[1])
		if pattern == "" {
			return nil, planeFault(layer, PlaneRules, row.line, "empty pattern")
		}
		modules := parseModuleIDs(row.cells[2])
		if len(modules) == 0 {
			return nil, planeFault(layer, PlaneRules, row.line, "%s %q names no module", kind, pattern)
		}
		description := ""
		if len(row.cells) > 3 {
			description = strings.TrimSpace(strings.Join(row.cells[3:], "\t"))
		}
		out = append(out, CapabilityRule{Kind: kind, Pattern: pattern, Modules: modules, Description: description})
	}
	return out, nil
}

func parseREST(rows []tsvRow, layer Layer) ([]RestOperation, *contract.Fault) {
	var out []RestOperation
	for _, row := range rows {
		if len(row.cells) < 4 {
			return nil, planeFault(layer, PlaneREST, row.line,
				"expected <method><TAB><path><TAB><owner_kind><TAB><module_ids>, got %d column(s)", len(row.cells))
		}
		method := strings.ToUpper(strings.TrimSpace(row.cells[0]))
		if !validMethod(method) {
			return nil, planeFault(layer, PlaneREST, row.line, "%q is not an HTTP method", row.cells[0])
		}
		path := strings.TrimSpace(row.cells[1])
		if !strings.HasPrefix(path, "/") {
			return nil, planeFault(layer, PlaneREST, row.line, "path %q is not a path template", path)
		}
		owner := OwnerKind(strings.TrimSpace(row.cells[2]))
		modules := parseModuleIDs(row.cells[3])
		switch owner {
		case OwnerPlatform:
			if len(modules) > 0 {
				return nil, planeFault(layer, PlaneREST, row.line,
					"%s %s is platform-owned but names modules %s", method, path, strings.Join(modules, ","))
			}
		case OwnerModule, OwnerPrivateModule:
			if len(modules) == 0 {
				return nil, planeFault(layer, PlaneREST, row.line,
					"%s %s is %s-owned but names no module", method, path, owner)
			}
		default:
			return nil, planeFault(layer, PlaneREST, row.line,
				"%s %s has owner_kind %q, want platform, module, or private-module", method, path, owner)
		}
		out = append(out, RestOperation{Method: method, Path: path, Owner: owner, Modules: modules})
	}
	return out, nil
}

// functionName reports whether name is a Gateway-scope system.* function name:
// system followed by at least two identifier segments.
func functionName(name string) bool {
	if !strings.HasPrefix(name, "system.") {
		return false
	}
	segments := strings.Split(name, ".")
	if len(segments) < 3 || segments[0] != "system" {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for j, r := range segment {
			switch {
			case r == '_':
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			case r >= '0' && r <= '9' && j > 0:
			default:
				return false
			}
		}
	}
	return true
}
