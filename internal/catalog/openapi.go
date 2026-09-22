package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// This file ports the legacy scripts/generate-rest-catalog.py, which stays the
// executable specification for the mapping rules: the route prefixes, the
// resource-module aliases, the classification order, and the row ordering are the
// generator's. Its output is now the REST plane of the Core Catalog itself
// (internal/catalog/assets/<version>/rest-endpoints.tsv), so what
// `catalog import-openapi` writes is the overlay layer: the endpoints this
// binary's Core Catalog does not already carry.

// openAPIMethods are the path-item members the legacy generator recognized, in its
// order. The match is case-sensitive, exactly as the generator's was: an OpenAPI
// document spells its operations in lower case, and a member it does not spell
// that way declares nothing.
var openAPIMethods = []string{"delete", "get", "head", "options", "patch", "post", "put", "trace"}

// openAPIOwner is a classified endpoint owner: the kind and the module ids that
// serve it. A platform endpoint names no module.
type openAPIOwner struct {
	Owner   OwnerKind
	Modules []string
}

// routeModule is one entry of the generator's route table: a path prefix that owns
// every operation beneath it.
type routeModule struct {
	prefix string
	owner  openAPIOwner
}

// moduleOwner and privateModuleOwner build the two owners a route row can name.
func moduleOwner(ids ...string) openAPIOwner {
	return openAPIOwner{Owner: OwnerModule, Modules: ids}
}

func privateModuleOwner(ids ...string) openAPIOwner {
	return openAPIOwner{Owner: OwnerPrivateModule, Modules: ids}
}

// platformOwner is the owner of every path the route table and the resource API do
// not claim.
var platformOwner = openAPIOwner{Owner: OwnerPlatform}

// openAPIRouteModules is the generator's ROUTE_MODULES table, verbatim. A private
// module's routes are listed here because the module is licensed, never because
// the path is special: the table is data, and adding a module route is adding data.
var openAPIRouteModules = []routeModule{
	{"/data/alarm-notification/", moduleOwner("com.inductiveautomation.alarm-notification")},
	{"/data/eam/", moduleOwner("com.inductiveautomation.eam")},
	{"/data/event-stream/", moduleOwner("com.inductiveautomation.eventstream")},
	{"/data/fsql/", moduleOwner("com.inductiveautomation.sqlbridge")},
	{"/data/opc-ua/", moduleOwner("com.inductiveautomation.opcua")},
	{"/data/perspective/", moduleOwner("com.inductiveautomation.perspective")},
	{"/data/reporting/", moduleOwner("com.inductiveautomation.reporting")},
	{"/data/sfc/", moduleOwner("com.inductiveautomation.sfc")},
	{"/data/vision/", moduleOwner("com.inductiveautomation.vision")},
	{"/data/mcp/", privateModuleOwner("com.inductiveautomation.mcp")},
}

// openAPIResourcesPrefix is the resource API root; the module a resource belongs to
// is the first `com.*` segment below it.
const openAPIResourcesPrefix = "/data/api/v1/resources/"

// openAPIResourceAliases is the generator's RESOURCE_MODULE_ALIASES table: a
// resource module whose module id differs from the resource namespace, or that
// needs more than one module. Everything else resolves to its own namespace.
var openAPIResourceAliases = map[string]openAPIOwner{
	"com.inductiveautomation.mcp": privateModuleOwner("com.inductiveautomation.mcp"),
	"com.inductiveautomation.opcua.drivers.bacnet": moduleOwner(
		"com.inductiveautomation.opcua.drivers.bacnet",
		"com.inductiveautomation.opcua",
	),
	"com.inductiveautomation.sip-notification": moduleOwner("com.inductiveautomation.phone-notification"),
}

// OpenAPIOperation is one operation an OpenAPI document declares, classified the
// way the legacy generator classified it: who serves the path template and which
// modules the endpoint needs.
type OpenAPIOperation struct {
	// Method is the upper-case HTTP method.
	Method string
	// Path is the path template, with any `{param}` segment preserved.
	Path string
	// Owner is who serves the endpoint; Modules are the ids it requires. A
	// platform endpoint names no module.
	Owner   OwnerKind
	Modules []string
}

// ParseOpenAPI reads an OpenAPI 3 JSON document and returns its REST operations,
// sorted by path template and then method, exactly as the legacy generator sorted
// its rows.
//
// A document that cannot be read as JSON, or whose `paths` member is absent or not
// an object, is IGDEV_E_USAGE: the document is an argument, and a run that cannot
// read it has nothing to import. A document that declares no operation at all is
// refused for the same reason, and it is the one that would otherwise be
// destructive: importing it with --prune would empty a tracked overlay.
func ParseOpenAPI(raw []byte, name string) ([]OpenAPIOperation, *contract.Fault) {
	var document struct {
		Paths json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, openAPIFault(name, "is not valid JSON: %v", err)
	}
	paths := map[string]json.RawMessage{}
	if err := json.Unmarshal(document.Paths, &paths); err != nil {
		return nil, openAPIFault(name, "has no object-valued `paths` member")
	}

	operations := make([]OpenAPIOperation, 0, len(paths))
	for path, item := range paths {
		members := map[string]json.RawMessage{}
		if err := json.Unmarshal(item, &members); err != nil {
			// A path item that is not an object declares no operation, which is
			// also how the generator read it.
			continue
		}
		owner := classifyOpenAPIPath(path)
		for _, method := range openAPIMethods {
			if _, ok := members[method]; !ok {
				continue
			}
			operations = append(operations, OpenAPIOperation{
				Method:  strings.ToUpper(method),
				Path:    path,
				Owner:   owner.Owner,
				Modules: owner.Modules,
			})
		}
	}
	if len(operations) == 0 {
		return nil, openAPIFault(name, "declares no REST operations")
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].Path != operations[j].Path {
			return operations[i].Path < operations[j].Path
		}
		return operations[i].Method < operations[j].Method
	})
	return operations, nil
}

// ParseOpenAPIFile reads one OpenAPI document and parses it. The name is what the
// caller asked for, so a fault names the document the user typed, not the path the
// process resolved.
func ParseOpenAPIFile(path, name string) ([]OpenAPIOperation, []byte, *contract.Fault) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, openAPIFault(name, "cannot be read: %v", err)
	}
	operations, fault := ParseOpenAPI(raw, name)
	return operations, raw, fault
}

// openAPIFault reports an OpenAPI document the import cannot use.
func openAPIFault(name, format string, args ...any) *contract.Fault {
	return contract.UsageFault(
		fmt.Sprintf("igdev catalog import-openapi: %s %s", name, fmt.Sprintf(format, args...)),
		contract.Remediation{
			Command: "igdev help catalog import-openapi",
			Why:     "show what this command reads and where it writes",
		})
}

// classifyOpenAPIPath maps one path template to its owner, following the
// generator's classify_path: the route table first, then the resource API's module
// segment and its alias table, and the platform for everything else.
func classifyOpenAPIPath(path string) openAPIOwner {
	for _, route := range openAPIRouteModules {
		if strings.HasPrefix(path, route.prefix) {
			return route.owner
		}
	}
	rest, found := strings.CutPrefix(path, openAPIResourcesPrefix)
	if !found {
		return platformOwner
	}
	for _, segment := range strings.Split(rest, "/") {
		if !strings.HasPrefix(segment, "com.") {
			continue
		}
		if alias, ok := openAPIResourceAliases[segment]; ok {
			return alias
		}
		return moduleOwner(segment)
	}
	return platformOwner
}

// openAPIMarker introduces the block inside an overlay file that
// `catalog import-openapi` owns. It is a comment, so every other reader ignores it,
// and it is the handle a later import uses to replace exactly what it wrote: the
// REST plane of an overlay is generated, and the other planes are hand-authored.
const openAPIMarker = "# igdev catalog import-openapi"

// DefaultOverlayOutput is the overlay file an import writes when neither --output
// nor the Project Contract's [catalog].overlay_paths names a file.
const DefaultOverlayOutput = "catalog/rest-overlay.tsv"

// OpenAPIImport is the outcome of regenerating a Project Overlay's REST plane from
// one OpenAPI document.
type OpenAPIImport struct {
	// Content is the overlay file's new bytes.
	Content []byte
	// Operations is how many operations the document declares.
	Operations int
	// Written is how many rows this document contributes to the overlay.
	Written int
	// Added is how many of those rows the overlay did not already carry.
	Added int
	// Preserved is how many rows a previous import wrote that this document no
	// longer declares and that this run kept, because it was not asked to prune.
	Preserved int
	// Removed is how many rows a previous import wrote that this run dropped: the
	// ones --prune discarded, and the ones the Core Catalog has come to carry
	// itself, which the overlay may not repeat.
	Removed int
	// Redundant is how many of the document's operations the Core Catalog already
	// carries with the same owner, so the overlay has nothing to add.
	Redundant int
}

// ImportOpenAPI regenerates the REST plane of the Project Overlay at declared from
// an OpenAPI document, keeping whatever a previous import wrote that the document
// no longer declares unless prune asks for it to go.
//
// A row whose key the Core Catalog already holds with a different owner is
// IGDEV_E_OVERLAY_CONFLICT and nothing is written: an overlay may add knowledge
// and may not shadow it (ADR 0005). The rows the core already carries identically
// are not copied into the overlay — a duplicate row would be refused as a conflict
// on the next read — which is what makes importing a full `openapi.json` of an
// Ignition version this binary already knows a no-op instead of a wall of
// conflicts. That is also why a row a previous import wrote is dropped, with or
// without prune, once the Core Catalog has come to carry it.
func ImportOpenAPI(version string, existing []byte, operations []OpenAPIOperation, sourceDigest string, prune bool, declared string) (OpenAPIImport, *contract.Fault) {
	core, fault := Core(version)
	if fault != nil {
		return OpenAPIImport{}, fault
	}
	out := OpenAPIImport{Operations: len(operations)}
	coreRows := make(map[string]RestOperation, len(core.REST))
	for _, row := range core.REST {
		coreRows[restKey(row.Method, row.Path)] = row
	}

	written := make(map[string]RestOperation, len(operations))
	for _, op := range operations {
		row := RestOperation{Method: op.Method, Path: op.Path, Owner: op.Owner, Modules: op.Modules}
		key := restKey(row.Method, row.Path)
		if held, ok := coreRows[key]; ok {
			if ownerKey(held) == ownerKey(row) {
				out.Redundant++
				continue
			}
			return OpenAPIImport{}, duplicateFault("REST operation "+key, LayerCore, held, row, LayerOverlay)
		}
		if duplicate, ok := written[key]; ok {
			return OpenAPIImport{}, duplicateFault("REST operation "+key, LayerOverlay, duplicate, row, LayerOverlay)
		}
		written[key] = row
	}

	priorRows, start, end := openAPIBlock(existing)
	prior := make(map[string]generatedRow, len(priorRows))
	order := make([]string, 0, len(priorRows))
	for _, row := range priorRows {
		if _, ok := prior[row.key]; !ok {
			order = append(order, row.key)
		}
		prior[row.key] = row
	}

	merged := make(map[string]generatedRow, len(written)+len(prior))
	for key, row := range written {
		merged[key] = row.generated()
		if _, ok := prior[key]; !ok {
			out.Added++
		}
	}
	for _, key := range order {
		if _, ok := written[key]; ok {
			// This document declares it, so the document's row is the one in force.
			continue
		}
		if _, carried := coreRows[key]; carried {
			// The Core Catalog carries it now: keeping it would rebuild the
			// conflict the overlay may not have.
			out.Removed++
			continue
		}
		if prune {
			out.Removed++
			continue
		}
		merged[key] = prior[key]
		out.Preserved++
	}

	rows := make([]generatedRow, 0, len(merged))
	for _, row := range merged {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].path != rows[j].path {
			return rows[i].path < rows[j].path
		}
		return rows[i].method < rows[j].method
	})
	out.Written = len(written)
	out.Content = spliceOpenAPIBlock(existing, start, end, renderOpenAPIBlock(version, sourceDigest, rows))

	// The candidate is validated before it is handed back for writing: a file an
	// import would have to refuse must never reach the disk, not even as a partial
	// write. This is where a hand-authored REST row in the same overlay that
	// shadows a core row is caught.
	candidate, fault := overlayFromBytes(declared, out.Content)
	if fault != nil {
		return OpenAPIImport{}, fault
	}
	if _, fault := New(version, candidate); fault != nil {
		return OpenAPIImport{}, fault
	}
	return out, nil
}

// generated converts a REST row into the overlay line it renders as.
func (r RestOperation) generated() generatedRow {
	return generatedRow{
		key:    restKey(r.Method, r.Path),
		path:   r.Path,
		method: r.Method,
		text:   r.tsv(),
	}
}

// generatedRow is one REST row as the overlay carries it: its resolution key, the
// cells that order it, and the line it renders as.
type generatedRow struct {
	key    string
	path   string
	method string
	text   string
}

// restRowOf interprets one overlay line as a REST row. A line that is not one keeps
// its text as its key, so a malformed row survives to be reported by the reader
// that owns the format instead of being silently dropped here.
func restRowOf(line string) generatedRow {
	cells := strings.Split(line, "\t")
	if len(cells) < 2 {
		return generatedRow{key: line, path: line, text: line}
	}
	method := strings.ToUpper(strings.TrimSpace(cells[0]))
	path := strings.TrimSpace(cells[1])
	return generatedRow{key: restKey(method, path), path: path, method: method, text: line}
}

// openAPIBlock locates the block a previous import wrote and returns its rows in
// file order plus the line range it occupies. start is -1 when the file carries no
// such block, which is the hand-authored overlay's case.
//
// The block runs from the marker line to the line before the next plane directive
// or the next marker, so a hand-authored row in a block of its own is never
// touched.
func openAPIBlock(existing []byte) (rows []generatedRow, start, end int) {
	lines := fileLines(existing)
	start = -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), openAPIMarker) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, -1, -1
	}
	directive := false
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, openAPIMarker) {
			end = i
			break
		}
		if strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(trimmed, "#")), overlayDirective) {
			if directive {
				end = i
				break
			}
			directive = true
		}
	}
	for _, line := range lines[start:end] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		rows = append(rows, restRowOf(strings.TrimSuffix(line, "\r")))
	}
	return rows, start, end
}

// renderOpenAPIBlock renders the block `catalog import-openapi` owns: the source
// digest, the plane directive, and one row per endpoint, in path order.
func renderOpenAPIBlock(version, sourceDigest string, rows []generatedRow) []string {
	lines := []string{
		openAPIMarker + ": generated REST plane — regenerate it with `igdev catalog import-openapi`.",
		"# ignition_version=" + version,
		"# source_sha256=" + strings.TrimPrefix(sourceDigest, "sha256:"),
		"# columns: method<TAB>path_template<TAB>owner_kind(platform|module|private-module)<TAB>required_module_ids(comma-separated or -)",
		"# " + overlayDirective + " " + string(KindREST),
	}
	for _, row := range rows {
		lines = append(lines, row.text)
	}
	return lines
}

// spliceOpenAPIBlock replaces the block's line range with the generated one, or
// appends the block to a file that has none, and returns the new file content. The
// lines around the block are preserved byte for byte; only the blank-line
// separators the block needs are introduced.
func spliceOpenAPIBlock(existing []byte, start, end int, block []string) []byte {
	lines := fileLines(existing)
	var out []string
	if start >= 0 {
		out = append(out, lines[:start]...)
		out = append(out, block...)
		rest := lines[end:]
		if len(rest) > 0 && strings.TrimSpace(rest[0]) != "" {
			out = append(out, "")
		}
		out = append(out, rest...)
	} else {
		out = append(out, lines...)
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, block...)
	}
	return []byte(joinLines(out))
}

// fileLines splits a file into lines without inventing one for an empty file.
func fileLines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(string(raw), "\n")
}

// joinLines renders lines as a file: no trailing blank lines, exactly one newline
// at the end.
func joinLines(lines []string) string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
