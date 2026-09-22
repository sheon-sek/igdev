package catalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Effective is the resolution of the Core Catalog with the Project Overlay: what
// preflight consults. The two layers stay distinguishable, so a resolution can
// say which one answered (ADR 0005).
type Effective struct {
	version       string
	core          *Catalog
	overlay       *Catalog
	coreDigest    string
	overlayDigest string
	overlayPaths  []string

	functions map[string]layerRow[NativeFunction]
	rules     []layerRow[CapabilityRule]
	ruleKeys  map[string]layerRow[CapabilityRule]
	rest      []layerRow[RestOperation]
	restKeys  map[string]layerRow[RestOperation]
	builtins  map[string]layerRow[BuiltinModule]
	overlayBM []BuiltinModule
}

// layerRow is one catalog row together with the layer it came from.
type layerRow[T any] struct {
	layer Layer
	value T
}

// tsv is a row that can render itself in the overlay file format, so a conflict
// message quotes a row the author can find in their file.
type tsv interface{ tsv() string }

func (r BuiltinModule) tsv() string { return r.ID + "\t" + r.Artifact }

func (r NativeFunction) tsv() string {
	return r.Function + "\t" + string(r.Class) + "\t" + strings.Join(r.Modules, ",") + "\t" + r.Note
}

func (r CapabilityRule) tsv() string {
	return string(r.Kind) + "\t" + r.Pattern + "\t" + strings.Join(r.Modules, ",") + "\t" + r.Description
}

func (r RestOperation) tsv() string {
	modules := strings.Join(r.Modules, ",")
	if len(r.Modules) == 0 {
		modules = "-"
	}
	return r.Method + "\t" + r.Path + "\t" + string(r.Owner) + "\t" + modules
}

// New builds the Effective Catalog for one Ignition version. The overlay may add
// rows the core does not have; a row whose key the core already holds is
// IGDEV_E_OVERLAY_CONFLICT, because an overlay that silently changes the meaning
// of a core row is not reviewable.
func New(version string, overlay *Overlay) (*Effective, *contract.Fault) {
	core, err := Core(version)
	if err != nil {
		return nil, err
	}
	coreDigest, err := CoreDigest(version)
	if err != nil {
		return nil, err
	}
	if overlay == nil {
		overlay = &Overlay{Catalog: &Catalog{}, Digest: digest(nil)}
	}
	added := overlay.Catalog
	if added == nil {
		added = &Catalog{}
	}

	e := &Effective{
		version:       version,
		core:          core,
		overlay:       added,
		coreDigest:    coreDigest,
		overlayDigest: overlay.Digest,
		overlayPaths:  overlay.Paths,
		functions:     map[string]layerRow[NativeFunction]{},
		ruleKeys:      map[string]layerRow[CapabilityRule]{},
		restKeys:      map[string]layerRow[RestOperation]{},
		builtins:      map[string]layerRow[BuiltinModule]{},
	}
	if fault := e.merge(core, LayerCore); fault != nil {
		return nil, fault
	}
	if fault := e.merge(added, LayerOverlay); fault != nil {
		return nil, fault
	}
	return e, nil
}

// merge folds one layer into the indexes, refusing a row whose key another row
// already holds. A duplicate inside the Core Catalog is a build defect; an
// overlay row that shadows a row in force is the project's to fix, and both rows
// are named so that it is fixable.
func (e *Effective) merge(o *Catalog, layer Layer) *contract.Fault {
	for _, row := range o.Functions {
		if existing, ok := e.functions[row.Function]; ok {
			return duplicateFault("native function "+row.Function, existing.layer, existing.value, row, layer)
		}
		e.functions[row.Function] = layerRow[NativeFunction]{layer, row}
	}
	for _, row := range o.Rules {
		if existing, ok := e.ruleKeys[ruleKey(row)]; ok {
			return duplicateFault("capability rule "+keyOfRule(row), existing.layer, existing.value, row, layer)
		}
		mark := layerRow[CapabilityRule]{layer, row}
		e.rules = append(e.rules, mark)
		e.ruleKeys[ruleKey(row)] = mark
	}
	for _, row := range o.REST {
		if existing, ok := e.restKeys[restKey(row.Method, row.Path)]; ok {
			return duplicateFault("REST operation "+restKey(row.Method, row.Path), existing.layer, existing.value, row, layer)
		}
		mark := layerRow[RestOperation]{layer, row}
		e.rest = append(e.rest, mark)
		e.restKeys[restKey(row.Method, row.Path)] = mark
	}
	for _, row := range o.BuiltinModules {
		if existing, ok := e.builtins[row.ID]; ok {
			return duplicateFault("built-in module "+row.ID, existing.layer, existing.value, row, layer)
		}
		e.builtins[row.ID] = layerRow[BuiltinModule]{layer, row}
		if layer == LayerOverlay {
			e.overlayBM = append(e.overlayBM, row)
		}
	}
	return nil
}

// duplicateFault names both rows: the one already in force and the incoming row
// that would shadow it.
func duplicateFault(key string, existingLayer Layer, existing, incoming tsv, incomingLayer Layer) *contract.Fault {
	if incomingLayer == LayerCore {
		return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("core catalog row %q duplicates the row %q for %s", incoming.tsv(), existing.tsv(), key))
	}
	return contract.NewFault(contract.CodeOverlayConflict, contract.ExitFailure,
		fmt.Sprintf("project overlay row %q conflicts with the %s row %q for %s; an overlay may add knowledge but must not shadow it",
			incoming.tsv(), existingLayer, existing.tsv(), key)).
		WithRemediation(contract.Remediation{
			Command: "igdev catalog status --json",
			Why:     "report the Core Catalog and Project Overlay layers with their digests",
		})
}

// Version is the Ignition version this Effective Catalog resolves for.
func (e *Effective) Version() string { return e.version }

// CoreDigest and OverlayDigest are the per-layer digests `catalog status`
// reports. The overlay digest covers its declared files in order.
func (e *Effective) CoreDigest() string    { return e.coreDigest }
func (e *Effective) OverlayDigest() string { return e.overlayDigest }

// OverlayPaths lists the declared overlay files, in resolution order.
func (e *Effective) OverlayPaths() []string { return append([]string(nil), e.overlayPaths...) }

// CoreCounts and OverlayCounts are the per-layer row counts. They are reported
// for a human reading the layers; integrity is the digest (ADR 0005).
func (e *Effective) CoreCounts() Counts    { return e.core.Counts() }
func (e *Effective) OverlayCounts() Counts { return e.overlay.Counts() }

// Counts is the Effective Catalog's row counts, layer by layer.
func (e *Effective) Counts() Counts { return addCounts(e.CoreCounts(), e.OverlayCounts()) }

// addCounts sums two layers' counts.
func addCounts(a, b Counts) Counts {
	return Counts{
		BuiltinModules:  a.BuiltinModules + b.BuiltinModules,
		NativeFunctions: a.NativeFunctions + b.NativeFunctions,
		CapabilityRules: a.CapabilityRules + b.CapabilityRules,
		RestOperations:  a.RestOperations + b.RestOperations,
	}
}

// Builtin reports whether a module id ships in the Ignition image.
func (e *Effective) Builtin(id string) bool {
	_, ok := e.builtins[id]
	return ok
}

// BuiltinModules lists the built-in modules, core rows first.
func (e *Effective) BuiltinModules() []BuiltinModule {
	out := make([]BuiltinModule, 0, len(e.core.BuiltinModules)+len(e.overlayBM))
	out = append(out, e.core.BuiltinModules...)
	out = append(out, e.overlayBM...)
	return out
}

// Resolve maps one capability to its owner(s). The lookup order is the bash
// specification's cap_modules: an explicit module, a REST method and path, a
// native function, then a capability rule.
func (e *Effective) Resolve(capability string) (Resolution, *contract.Fault) {
	c := strings.TrimSpace(capability)
	if c == "" {
		return Resolution{Capability: c, Layer: LayerCore},
			fault(contract.CodeUnknownCapability, "no mapping for an empty capability")
	}
	if id, ok := strings.CutPrefix(c, "module:"); ok && id != "" {
		return Resolution{Capability: c, Kind: KindModule, Modules: []string{id}, Layer: LayerCore}, nil
	}
	if strings.HasPrefix(c, "com.") {
		return Resolution{Capability: c, Kind: KindModule, Modules: []string{c}, Layer: LayerCore}, nil
	}
	if request, ok := ParseRESTRequest(c); ok {
		return e.resolveREST(c, request)
	}
	if strings.HasPrefix(c, "system.") {
		return e.resolveFunction(c)
	}
	return e.resolveRule(c)
}

// resolveFunction answers a system.* capability from the native function plane.
func (e *Effective) resolveFunction(name string) (Resolution, *contract.Fault) {
	row, ok := e.functions[name]
	if !ok {
		return Resolution{Capability: name, Kind: KindNativeFunction, Layer: LayerCore}, unknownCapability(name)
	}
	out := Resolution{
		Capability: name,
		Kind:       KindNativeFunction,
		Layer:      row.layer,
		Class:      row.value.Class,
		Note:       row.value.Note,
	}
	// The platform classification means "ships with the platform"; a row that
	// names no module means the same thing whatever its classification says.
	if row.value.Class == ClassPlatform || len(row.value.Modules) == 0 {
		out.Platform = true
		return out, nil
	}
	out.Modules = append([]string(nil), row.value.Modules...)
	return out, nil
}

// resolveREST answers a REST capability. One concrete path may match several
// templates (one per param position) and those templates may name different
// owners; preflight cannot choose for the caller, so that is a fault naming the
// candidates.
func (e *Effective) resolveREST(capability string, request RESTRequest) (Resolution, *contract.Fault) {
	out := Resolution{Capability: capability, Kind: KindREST, Method: request.Method}
	var matches []layerRow[RestOperation]
	for _, row := range e.rest {
		if request.Method != "" && row.value.Method != request.Method {
			continue
		}
		if !pathMatches(row.value.Path, request.Path) {
			continue
		}
		matches = append(matches, row)
	}
	if len(matches) == 0 {
		return out, fault(contract.CodeUnknownCapability,
			"REST operation is not present in the Ignition %s catalog: %s", Target, capability)
	}
	// One concrete path can match several templates — a bare path matches every
	// method, and a "{param}" template matches a literal path. Pick the most
	// specific one, then the earliest method, so the reported template is a
	// stable answer rather than a catalog-order accident.
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i].value, matches[j].value
		if la, lb := literalSegments(a.Path), literalSegments(b.Path); la != lb {
			return la > lb
		}
		if ra, rb := methodRank(a.Method), methodRank(b.Method); ra != rb {
			return ra < rb
		}
		return a.Path < b.Path
	})
	owners := map[string]bool{}
	for _, row := range matches {
		owners[ownerKey(row.value)] = true
	}
	if len(owners) > 1 {
		rows := make([]string, 0, len(matches))
		for _, row := range matches {
			rows = append(rows, row.value.tsv())
		}
		sort.Strings(rows)
		return out, fault(contract.CodeCapabilityAmbiguous,
			"ambiguous REST ownership for %s; the catalog rows %q disagree", capability, strings.Join(rows, " | "))
	}
	chosen := matches[0]
	out.Layer = chosen.layer
	out.Template = chosen.value.Path
	if chosen.value.Owner == OwnerPlatform {
		out.Platform = true
		return out, nil
	}
	out.Modules = append([]string(nil), chosen.value.Modules...)
	return out, nil
}

// resolveRule answers a capability from the capability rule plane: exact rules
// first, then prefix rules, each pass in catalog order with core rows before
// overlay rows.
func (e *Effective) resolveRule(capability string) (Resolution, *contract.Fault) {
	out := Resolution{Capability: capability, Kind: KindAlias, Layer: LayerCore}
	for _, want := range []RuleKind{RuleExact, RulePrefix} {
		for _, row := range e.rules {
			if row.value.Kind != want {
				continue
			}
			if want == RuleExact && capability != row.value.Pattern {
				continue
			}
			if want == RulePrefix && !strings.HasPrefix(capability, row.value.Pattern) {
				continue
			}
			out.Layer = row.layer
			out.Modules = append([]string(nil), row.value.Modules...)
			return out, nil
		}
	}
	return out, unknownCapability(capability)
}

// unknownCapability keeps the bash specification's wording, which tells the
// reader which plane was consulted and what to check.
func unknownCapability(c string) *contract.Fault {
	if strings.HasPrefix(c, "system.") {
		return fault(contract.CodeUnknownCapability,
			"unknown or non-Gateway Ignition %s native function: %s. Check the function name, Gateway scope, and target Ignition version.",
			series(Target), c)
	}
	return fault(contract.CodeUnknownCapability, "no mapping for %s", c)
}

// fault builds a catalog fault at exit level 1.
func fault(code contract.Code, format string, args ...any) *contract.Fault {
	return contract.NewFault(code, contract.ExitFailure, fmt.Sprintf(format, args...))
}

// ruleKey is a capability rule's identity for conflict detection.
func ruleKey(row CapabilityRule) string { return string(row.Kind) + " " + row.Pattern }

// keyOfRule renders a capability rule's identity for a message.
func keyOfRule(row CapabilityRule) string { return fmt.Sprintf("%s %q", row.Kind, row.Pattern) }

// ownerKey is a REST row's owner identity for ambiguity detection.
func ownerKey(row RestOperation) string {
	return string(row.Owner) + " " + strings.Join(row.Modules, ",")
}

// literalSegments counts a template's non-placeholder segments, which is how
// specific a template is: "/data/x/y" beats "/data/x/{p}".
func literalSegments(template string) int {
	count := 0
	for _, segment := range strings.Split(strings.TrimSuffix(template, "/"), "/") {
		if !isParam(segment) {
			count++
		}
	}
	return count
}

// methodRank is a method's position in the accepted method order, used only as a
// tie-break: a bare path that matches several methods reports GET first.
func methodRank(method string) int {
	for i, candidate := range Methods {
		if candidate == method {
			return i
		}
	}
	return len(Methods)
}
