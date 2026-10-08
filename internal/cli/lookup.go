package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/lookup"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
	"github.com/sheon-sek/igdev/internal/xdg"
)

// lookupData is what `igdev lookup "<query>"` reports.
type lookupData struct {
	Query           string         `json:"query"`
	Kind            string         `json:"kind"`
	IgnitionVersion string         `json:"ignition_version"`
	Indexes         lookupIndexes  `json:"indexes"`
	Results         []lookupResult `json:"results"`
}

// lookupResult is one search hit, with how to use it here.
type lookupResult struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Score   float64 `json:"score"`
	Summary string  `json:"summary"`
	// Module is "platform", the module id, or the ids joined by commas when the
	// function needs several; "unknown" for an endpoint no catalog row owns.
	Module string `json:"module"`
	// ModuleEnabled is absent outside a Project Root, where there is no
	// whitelist to answer it.
	ModuleEnabled *bool `json:"module_enabled,omitempty"`
	// Scope is a function's: all, or the scopes joined by commas.
	Scope string `json:"scope,omitempty"`
	// Call is an endpoint's ready-to-run igdev gateway api line.
	Call   string `json:"call,omitempty"`
	Source string `json:"source"`
}

// lookupIndexes says which indexes answered and where they came from.
type lookupIndexes struct {
	Functions *lookupFunctionIndex `json:"functions,omitempty"`
	REST      *lookupRESTIndex     `json:"rest,omitempty"`
}

type lookupFunctionIndex struct {
	Path    string `json:"path"`
	Image   string `json:"image"`
	ImageID string `json:"image_id"`
	// Built is true when this run built the index rather than reading it.
	Built      bool `json:"built"`
	Functions  int  `json:"functions"`
	Documented int  `json:"documented"`
	// PrivateModules lists the staged module archives whose bundles were read.
	PrivateModules []lookupModuleIndex `json:"private_modules"`
}

type lookupModuleIndex struct {
	Artifact  string `json:"artifact"`
	SHA256    string `json:"sha256"`
	Functions int    `json:"functions"`
}

type lookupRESTIndex struct {
	Path      string `json:"path"`
	Source    string `json:"source"`
	SHA256    string `json:"sha256,omitempty"`
	Built     bool   `json:"built"`
	Endpoints int    `json:"endpoints"`
}

// lookupRefreshData is what `igdev lookup --refresh` alone reports.
type lookupRefreshData struct {
	IgnitionVersion string        `json:"ignition_version"`
	Indexes         lookupIndexes `json:"indexes"`
}

// lookupFunctionDetail is `--name` for a function: the whole entry.
type lookupFunctionDetail struct {
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Scope         string         `json:"scope"`
	Scopes        []string       `json:"scopes"`
	Module        string         `json:"module"`
	ModuleEnabled *bool          `json:"module_enabled,omitempty"`
	Description   string         `json:"description"`
	Params        []lookup.Param `json:"params"`
	Returns       string         `json:"returns,omitempty"`
	Deprecated    string         `json:"deprecated,omitempty"`
	Replacement   string         `json:"replacement,omitempty"`
	Note          string         `json:"note,omitempty"`
	Docs          string         `json:"docs,omitempty"`
	Source        string         `json:"source"`
}

// lookupEndpointDetail is `--name` for an endpoint: parameters, request body and
// responses, with the schemas inlined.
type lookupEndpointDetail struct {
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Method        string         `json:"method"`
	Path          string         `json:"path"`
	Summary       string         `json:"summary,omitempty"`
	Description   string         `json:"description,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Module        string         `json:"module"`
	ModuleEnabled *bool          `json:"module_enabled,omitempty"`
	Params        []lookup.Param `json:"params"`
	RequestTypes  []string       `json:"request_types,omitempty"`
	RequestBody   map[string]any `json:"request_body,omitempty"`
	Responses     map[string]any `json:"responses,omitempty"`
	Call          string         `json:"call"`
	Source        string         `json:"source"`
}

// lookupContext is everything one lookup run answers from.
type lookupContext struct {
	version   string
	eff       *catalog.Effective
	set       *moduleSet
	store     lookup.Store
	functions []lookup.Function
	endpoints []lookup.Endpoint
	indexes   lookupIndexes
	// openAPI is the cached document the REST index was read from, for --name.
	openAPI string
}

const lookupDefaultLimit = 10

func (a *App) newLookupCmd() *cobra.Command {
	var (
		kind    string
		limit   int
		name    string
		refresh bool
	)
	cmd := &cobra.Command{
		Use:   `lookup [<query>...]`,
		Short: "Find a system.* function or a Gateway REST endpoint from one sentence",
		Long: `lookup searches two local indexes with an offline keyword search and says how to
use each result in this checkout.

The function index is read from an Ignition image already on this machine for the
contract's version: this Instance's own Gateway image, the official image, or any
other Gateway image igdev built (each is FROM the official one and keeps its jars).
It reads the documentation bundles in the image's own jars and in its built-in
modules (<fn>.desc, .param.<name>, .param.<name>.default,
.returns), each placed in its system.* namespace. Nothing starts and no EULA is
involved; the image is only opened. The .modl archives this checkout stages are
read the same way, so a private module documented the SDK way is found too. A
catalog function no bundle documents, such as system.perspective.*, is still
listed with its module and a documentation link.

The REST index is read from this Instance's running Gateway, from its own
/openapi.json (fetched as gateway api --output does), so every endpoint the
Gateway serves is there, private modules included. Without a running Gateway it
falls back to the REST plane this binary embeds: method, path and module only,
marked source embedded; run --refresh once a Gateway is up for the full index.

Both indexes are cached under the igdev cache directory, per Ignition version, and
built on first use; the REST index is rebuilt by itself when it is the embedded
fallback and a Gateway now answers. --refresh rebuilds both.

A name match ranks above a description match. Each result says how to use it
here: a function's scope and module, and whether this checkout enables that
module; an endpoint's module and a ready-to-run igdev gateway api line with the
Content-Type its request body needs. --name prints one entry in full: every
parameter with its default and the return value for a function, or the
parameters, request body schema and responses for an endpoint.

Outside a Project Root it still works, from the configured Ignition version, and
leaves module_enabled out.`,
		Example: `  igdev lookup "read tag values"
  igdev lookup "import project" --kind rest
  igdev lookup --name system.tag.readBlocking
  igdev lookup --name "POST /data/api/v1/projects/import/{name}"
  igdev lookup --refresh`,
		RunE: func(_ *cobra.Command, args []string) error {
			query := ""
			if len(args) > 0 {
				// Several words are one query: quoting it is not required.
				query = strings.TrimSpace(strings.Join(args, " "))
			}
			if query != "" && name != "" {
				return contract.UsageFault("igdev lookup takes a <query> or --name, not both",
					contract.Remediation{Command: `igdev lookup --name system.tag.readBlocking`, Why: "print one entry in full"})
			}
			if query == "" && name == "" && !refresh {
				return missingArgument("lookup", "query", `igdev lookup "read tag values"`, "say what you are looking for")
			}
			switch kind {
			case "", lookup.KindFunction, lookup.KindREST:
			default:
				return contract.UsageFault(fmt.Sprintf("--kind %q is not function or rest", kind),
					contract.Remediation{Command: `igdev lookup "read tag values" --kind function`, Why: "search one kind of entry"})
			}
			if limit < 1 {
				return contract.UsageFault(fmt.Sprintf("--limit %d is not a positive number", limit),
					contract.Remediation{Command: `igdev lookup "read tag values" --limit 5`, Why: "ask for at least one result"})
			}
			found, res, doc, eff, err := a.catalogContext()
			if err != nil {
				return err
			}
			lc := &lookupContext{version: eff.Version(), eff: eff, store: lookup.NewStore(xdg.Resolve().Cache, eff.Version())}
			var records []modules.Record
			if found.InProject() {
				k, err := stagedKnowledge(found, res, doc, eff)
				if err != nil {
					return err
				}
				records = k.records
				lc.set = &k.set
			}
			wantFunctions, wantREST := kind != lookup.KindREST, kind != lookup.KindFunction
			if name != "" {
				wantFunctions, wantREST = !looksLikeEndpoint(name), looksLikeEndpoint(name)
			}
			if refresh {
				wantFunctions, wantREST = true, true
			}
			if wantFunctions {
				if err := a.loadFunctions(lc, found, records, refresh); err != nil {
					return err
				}
			}
			if wantREST {
				a.loadREST(lc, found.InProject(), refresh)
			}
			switch {
			case name != "":
				return a.lookupName(lc, res, name)
			case query == "":
				data := lookupRefreshData{IgnitionVersion: lc.version, Indexes: lc.indexes}
				a.emit(res, data, func() { a.printLookupIndexes(lc.indexes) })
				return nil
			}
			hits := lookup.Search(lc.functions, lc.endpoints, query, kind, limit)
			data := lookupData{Query: query, Kind: kind, IgnitionVersion: lc.version, Indexes: lc.indexes, Results: []lookupResult{}}
			if data.Kind == "" {
				data.Kind = "all"
			}
			for _, hit := range hits {
				data.Results = append(data.Results, lc.result(hit))
			}
			a.emit(res, data, func() { a.printLookupResults(data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "search only function or rest entries")
	cmd.Flags().IntVar(&limit, "limit", lookupDefaultLimit, "the number of results to return")
	cmd.Flags().StringVar(&name, "name", "", `print one entry in full: a function name, or "METHOD /path"`)
	cmd.Flags().BoolVar(&refresh, "refresh", false, "rebuild the function and REST indexes first")
	return cmd
}

// looksLikeEndpoint reports whether a --name names an endpoint: "METHOD /path"
// or a bare path.
func looksLikeEndpoint(name string) bool {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "/") {
		return true
	}
	method, path, ok := strings.Cut(name, " ")
	return ok && validMethod(strings.ToUpper(method)) && strings.HasPrefix(strings.TrimSpace(path), "/")
}

// loadFunctions reads the function index, building it when it is missing or on
// --refresh, then adds the staged private modules and the catalog functions no
// bundle documents. An Ignition release tag is never rebuilt with other jars, so
// one index per version stays valid until --refresh asks for a new one.
func (a *App) loadFunctions(lc *lookupContext, found project.Found, records []modules.Record, refresh bool) error {
	var known []string
	for _, f := range lc.eff.Functions() {
		known = append(known, f.Function)
	}
	idx, cached := lc.store.LoadFunctions()
	built := false
	if !cached || refresh {
		image, imageID, err := lookupImage(found, lc.version)
		if err != nil {
			return err
		}
		a.stage("reading the documentation bundles of %s into the lookup index", image)
		scratch, err := os.MkdirTemp("", "igdev-lookup-")
		if err != nil {
			return contract.NewFault(contract.CodeInternal, contract.ExitFailure, "cannot create a scratch directory: "+err.Error()).WithCause(err)
		}
		defer os.RemoveAll(scratch)
		idx, err = lookup.BuildImageIndex(lc.version, image, imageID, known, scratch)
		if err != nil {
			return imageFault(lc.version, err)
		}
		if err := lc.store.SaveFunctions(idx); err != nil {
			return cacheFault(lc.store.FunctionsPath(), err)
		}
		built = true
	}
	root := found.Root
	info := &lookupFunctionIndex{Path: lc.store.FunctionsPath(), Image: idx.Image, ImageID: idx.ImageID, Built: built, PrivateModules: []lookupModuleIndex{}}
	functions := append([]lookup.Function(nil), idx.Functions...)
	have := map[string]bool{}
	for _, f := range functions {
		have[f.Name] = true
	}
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		file := filepath.Join(modules.Dir(root), record.Artifact)
		sha, err := lookup.FileSHA256(file)
		if err != nil {
			continue
		}
		key := lookup.ModuleKey(sha, known)
		mod, ok := lc.store.LoadModule(key)
		if !ok || refresh {
			scratch, err := os.MkdirTemp("", "igdev-lookup-")
			if err != nil {
				continue
			}
			mod, err = lookup.BuildModuleIndex(file, sha, key, known, scratch)
			os.RemoveAll(scratch)
			if err != nil {
				continue
			}
			_ = lc.store.SaveModule(mod)
		}
		info.PrivateModules = append(info.PrivateModules, lookupModuleIndex{Artifact: record.Artifact, SHA256: sha, Functions: len(mod.Functions)})
		for _, f := range mod.Functions {
			if !have[f.Name] {
				have[f.Name] = true
				functions = append(functions, f)
			}
		}
	}
	for _, row := range lc.eff.Functions() {
		if have[row.Function] {
			continue
		}
		module := lookup.Platform
		if len(row.Modules) > 0 {
			module = strings.Join(row.Modules, ",")
		}
		functions = append(functions, lookup.Function{
			Name: row.Function, Scopes: []string{lookup.ScopeGateway}, Params: []lookup.Param{},
			Module: module, Source: "catalog", Note: row.Note,
		})
	}
	sort.SliceStable(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })
	for _, f := range functions {
		if f.Source != "catalog" {
			info.Documented++
		}
	}
	info.Functions = len(functions)
	lc.functions = functions
	lc.indexes.Functions = info
	return nil
}

// loadREST reads the REST index: the cached one, unless it is missing, is the
// embedded fallback while this Instance's Gateway now answers, or --refresh
// asks for a new one. A Gateway that cannot be reached leaves the embedded
// fallback in place; lookup never fails for want of a running Gateway.
func (a *App) loadREST(lc *lookupContext, inProject, refresh bool) {
	idx, cached := lc.store.LoadREST()
	built := false
	if !cached || refresh || idx.Source == lookup.SourceEmbedded {
		if live, ok := a.liveREST(lc, inProject); ok {
			idx, built = live, true
		} else if !cached || refresh {
			idx, built = embeddedREST(lc), true
			_ = lc.store.SaveREST(idx)
		}
	}
	if idx.Source == lookup.SourceOpenAPI {
		lc.openAPI = lc.store.OpenAPIPath()
	}
	lc.endpoints = idx.Endpoints
	lc.indexes.REST = &lookupRESTIndex{Path: lc.store.RESTPath(), Source: idx.Source, SHA256: idx.SHA256, Built: built, Endpoints: len(idx.Endpoints)}
}

// liveREST fetches this Instance's /openapi.json into the cache, the way
// `gateway api --output` does, and indexes it. Anything short of a 200 with a
// readable document is "no live index", not an error.
func (a *App) liveREST(lc *lookupContext, inProject bool) (*lookup.RESTIndex, bool) {
	if !inProject {
		return nil, false
	}
	g, err := a.gatewayContext()
	if err != nil {
		return nil, false
	}
	resp, target, err := sendAPI(g, http.MethodGet, "/openapi.json", nil, nil)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	a.stage("reading the Gateway's OpenAPI document into the lookup index")
	if _, err := saveAPIResponse(resp, http.MethodGet, "/openapi.json", target, lc.store.OpenAPIPath()); err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(lc.store.OpenAPIPath())
	if err != nil {
		return nil, false
	}
	endpoints, err := lookup.ParseOpenAPI(raw)
	if err != nil {
		return nil, false
	}
	sha, _ := lookup.FileSHA256(lc.store.OpenAPIPath())
	idx := &lookup.RESTIndex{Version: lc.version, Source: lookup.SourceOpenAPI, SHA256: sha, Endpoints: endpoints}
	if err := lc.store.SaveREST(idx); err != nil {
		return nil, false
	}
	return idx, true
}

// embeddedREST is the fallback REST index: the Effective Catalog's REST plane.
func embeddedREST(lc *lookupContext) *lookup.RESTIndex {
	idx := &lookup.RESTIndex{Version: lc.version, Source: lookup.SourceEmbedded, Endpoints: []lookup.Endpoint{}}
	for _, row := range lc.eff.RESTOperations() {
		module := lookup.Platform
		if len(row.Modules) > 0 {
			module = strings.Join(row.Modules, ",")
		}
		idx.Endpoints = append(idx.Endpoints, lookup.EmbeddedEndpoint(row.Method, row.Path, module))
	}
	return idx
}

// result renders one hit with how to use it here.
func (lc *lookupContext) result(hit lookup.Hit) lookupResult {
	out := lookupResult{Kind: hit.Kind, Name: hit.Name, Score: hit.Score}
	if f := hit.Function; f != nil {
		out.Summary = summaryOf(f.Description)
		if f.Deprecated != "" {
			out.Summary = strings.TrimSpace("(deprecated) " + out.Summary)
		}
		out.Module, out.ModuleEnabled = lc.functionModule(f)
		out.Scope = lookup.ScopeLabel(f.Scopes)
		out.Source = f.Source
		return out
	}
	e := hit.Endpoint
	// An OpenAPI summary is usually a title ("Import Project"); the
	// description's first sentence says what the operation does.
	out.Summary = summaryOf(e.Description)
	if out.Summary == "" {
		out.Summary = e.Summary
	}
	out.Module, out.ModuleEnabled = lc.endpointModule(e)
	out.Call = apiCall(e)
	out.Source = e.Source
	return out
}

// functionModule is the module a function needs: the catalog's answer when it
// has one, otherwise the module whose archive documents it.
func (lc *lookupContext) functionModule(f *lookup.Function) (string, *bool) {
	if res, fault := lc.eff.Resolve(f.Name); fault == nil && res.Kind == catalog.KindNativeFunction {
		if res.Platform {
			return lookup.Platform, lc.enabled(nil)
		}
		return strings.Join(res.Modules, ","), lc.enabled(res.Modules)
	}
	if f.Module == lookup.Platform {
		return lookup.Platform, lc.enabled(nil)
	}
	return f.Module, lc.enabled(strings.Split(f.Module, ","))
}

// endpointModule is the module that serves an endpoint, from the catalog.
func (lc *lookupContext) endpointModule(e *lookup.Endpoint) (string, *bool) {
	if e.Module != "" {
		if e.Module == lookup.Platform {
			return e.Module, lc.enabled(nil)
		}
		return e.Module, lc.enabled(strings.Split(e.Module, ","))
	}
	if res, fault := lc.eff.Resolve(e.Method + " " + e.Path); fault == nil {
		if res.Platform {
			return lookup.Platform, lc.enabled(nil)
		}
		return strings.Join(res.Modules, ","), lc.enabled(res.Modules)
	}
	return "unknown", nil
}

// enabled answers whether this checkout loads every module in ids; nil outside a
// Project Root.
func (lc *lookupContext) enabled(ids []string) *bool {
	if lc.set == nil {
		return nil
	}
	ok := true
	for _, id := range ids {
		if !lc.set.Enabled(id) {
			ok = false
		}
	}
	return &ok
}

// apiCall is the igdev gateway api line that calls an endpoint, with the
// Content-Type its request body needs when that is not the JSON igdev sends by
// default.
func apiCall(e *lookup.Endpoint) string {
	call := fmt.Sprintf("igdev gateway api %s '%s'", e.Method, e.Path)
	if len(e.RequestTypes) == 0 {
		return call
	}
	contentType := e.RequestTypes[0]
	if strings.HasPrefix(contentType, "application/json") {
		return call + " --data @body.json"
	}
	return call + fmt.Sprintf(" --header 'Content-Type: %s' --data @%s", contentType, bodyFile(contentType))
}

// bodyFile is the placeholder file name a call line uses for a body type.
func bodyFile(contentType string) string {
	switch {
	case strings.Contains(contentType, "zip"):
		return "body.zip"
	case strings.Contains(contentType, "xml"):
		return "body.xml"
	case strings.HasPrefix(contentType, "text/"):
		return "body.txt"
	}
	return "body.bin"
}

// summaryOf is the first sentence of a description, on one line.
func summaryOf(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}

// lookupName prints one entry in full.
func (a *App) lookupName(lc *lookupContext, res *config.Resolution, name string) error {
	name = strings.TrimSpace(name)
	if looksLikeEndpoint(name) {
		method, path, ok := strings.Cut(name, " ")
		if !ok {
			method, path = "", name
		}
		method, path = strings.ToUpper(method), strings.TrimSpace(path)
		for i := range lc.endpoints {
			e := &lc.endpoints[i]
			if (method == "" || e.Method == method) && templateMatches(e.Path, path) {
				return a.emitEndpointDetail(lc, res, e)
			}
		}
		return unknownEntry(name, "rest")
	}
	for i := range lc.functions {
		if lc.functions[i].Name == name {
			return a.emitFunctionDetail(lc, res, &lc.functions[i])
		}
	}
	return unknownEntry(name, "function")
}

func unknownEntry(name, kind string) error {
	return contract.NewFault(contract.CodeUnknownCapability, contract.ExitFailure,
		fmt.Sprintf("no %s named %q in the lookup index", kind, name)).
		WithRemediation(contract.Remediation{Command: fmt.Sprintf("igdev lookup %q --kind %s", name, kind), Why: "search for the closest names"})
}

// templateMatches reports whether a path names the template: the template
// itself, or a concrete path whose {param} segments are filled in.
func templateMatches(template, path string) bool {
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	t := strings.Split(strings.TrimSuffix(template, "/"), "/")
	p := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(t) != len(p) {
		return false
	}
	for i := range t {
		if strings.HasPrefix(t[i], "{") && strings.HasSuffix(t[i], "}") {
			continue
		}
		if t[i] != p[i] {
			return false
		}
	}
	return true
}

// docsLink is a system.* function's page in the Ignition user manual.
func docsLink(version, name string) string {
	if !strings.HasPrefix(name, "system.") {
		return ""
	}
	i := strings.LastIndex(name, ".")
	series := version
	if parts := strings.Split(version, "."); len(parts) >= 2 {
		series = parts[0] + "." + parts[1]
	}
	return fmt.Sprintf("https://www.docs.inductiveautomation.com/docs/%s/appendix/scripting-functions/%s/%s",
		series, strings.ReplaceAll(name[:i], ".", "-"), strings.ReplaceAll(name, ".", "-"))
}

func (a *App) emitFunctionDetail(lc *lookupContext, res *config.Resolution, f *lookup.Function) error {
	module, enabled := lc.functionModule(f)
	data := lookupFunctionDetail{
		Kind: lookup.KindFunction, Name: f.Name, Scope: lookup.ScopeLabel(f.Scopes), Scopes: f.Scopes,
		Module: module, ModuleEnabled: enabled, Description: f.Description, Params: f.Params,
		Returns: f.Returns, Deprecated: f.Deprecated, Replacement: f.Replacement, Note: f.Note,
		Docs: docsLink(lc.version, f.Name), Source: f.Source,
	}
	a.emit(res, data, func() {
		w := a.Stdout
		fmt.Fprintf(w, "%s  (function; scope %s; module %s%s)\n", data.Name, data.Scope, data.Module, enabledMark(enabled))
		if data.Deprecated != "" {
			fmt.Fprintf(w, "  deprecated: %s", data.Deprecated)
			if data.Replacement != "" {
				fmt.Fprintf(w, " Use %s.", data.Replacement)
			}
			fmt.Fprintln(w)
		}
		if data.Description != "" {
			fmt.Fprintf(w, "\n%s\n", data.Description)
		}
		if len(data.Params) > 0 {
			fmt.Fprintln(w, "\nParameters:")
			for _, p := range data.Params {
				extra := ""
				if p.Default != "" {
					extra = " (default " + p.Default + ")"
				} else if p.Optional {
					extra = " (optional)"
				}
				fmt.Fprintf(w, "  %s%s: %s\n", p.Name, extra, p.Description)
			}
		}
		if data.Returns != "" {
			fmt.Fprintf(w, "\nReturns: %s\n", data.Returns)
		}
		if data.Note != "" {
			fmt.Fprintf(w, "\nNote: %s\n", data.Note)
		}
		if data.Docs != "" {
			fmt.Fprintf(w, "\nDocs: %s\n", data.Docs)
		}
		fmt.Fprintf(w, "Source: %s\n", data.Source)
	})
	return nil
}

func (a *App) emitEndpointDetail(lc *lookupContext, res *config.Resolution, e *lookup.Endpoint) error {
	module, enabled := lc.endpointModule(e)
	data := lookupEndpointDetail{
		Kind: lookup.KindREST, Name: e.Name(), Method: e.Method, Path: e.Path, Summary: e.Summary,
		Description: e.Description, Tags: e.Tags, Module: module, ModuleEnabled: enabled, Params: e.Params,
		RequestTypes: e.RequestTypes, Call: apiCall(e), Source: e.Source,
	}
	if lc.openAPI != "" {
		if raw, err := os.ReadFile(lc.openAPI); err == nil {
			if detail, ok := lookup.Detail(raw, e.Method, e.Path); ok {
				data.RequestBody, data.Responses = detail.RequestBody, detail.Responses
			}
		}
	}
	a.emit(res, data, func() {
		w := a.Stdout
		fmt.Fprintf(w, "%s  (rest; module %s%s)\n", data.Name, data.Module, enabledMark(enabled))
		if data.Summary != "" {
			fmt.Fprintf(w, "\n%s\n", data.Summary)
		}
		if data.Description != "" && data.Description != data.Summary {
			fmt.Fprintf(w, "%s\n", data.Description)
		}
		if len(data.Params) > 0 {
			fmt.Fprintln(w, "\nParameters:")
			for _, p := range data.Params {
				required := ""
				if p.Required {
					required = ", required"
				}
				fmt.Fprintf(w, "  %s (%s%s): %s\n", p.Name, p.In, required, p.Description)
			}
		}
		if len(data.RequestTypes) > 0 {
			fmt.Fprintf(w, "\nRequest body: %s\n", strings.Join(data.RequestTypes, ", "))
		}
		if len(data.Responses) > 0 {
			codes := make([]string, 0, len(data.Responses))
			for code := range data.Responses {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			fmt.Fprintf(w, "Responses: %s (schemas in --json)\n", strings.Join(codes, ", "))
		}
		if data.Source == lookup.SourceEmbedded {
			fmt.Fprintln(w, "\nOnly method, path and module are known: start the Gateway and run igdev lookup --refresh for the full entry.")
		}
		fmt.Fprintf(w, "\nCall: %s\n", data.Call)
	})
	return nil
}

func enabledMark(enabled *bool) string {
	if enabled != nil && !*enabled {
		return ", not enabled in this checkout"
	}
	return ""
}

func (a *App) printLookupResults(data lookupData) {
	w := a.Stdout
	if len(data.Results) == 0 {
		fmt.Fprintf(w, "nothing in the lookup index matches %q\n", data.Query)
	}
	for i, r := range data.Results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		switch r.Kind {
		case lookup.KindFunction:
			fmt.Fprintf(w, "%s  (function; scope %s; module %s%s)\n", r.Name, r.Scope, r.Module, enabledMark(r.ModuleEnabled))
		default:
			fmt.Fprintf(w, "%s  (rest; module %s%s)\n", r.Name, r.Module, enabledMark(r.ModuleEnabled))
		}
		if r.Summary != "" {
			fmt.Fprintf(w, "  %s\n", r.Summary)
		}
		if r.Call != "" {
			fmt.Fprintf(w, "  %s\n", r.Call)
		}
	}
	a.printLookupNotes(data.Indexes)
}

// printLookupNotes says on stderr when an index is the reduced one.
func (a *App) printLookupNotes(idx lookupIndexes) {
	if idx.REST != nil && idx.REST.Source == lookup.SourceEmbedded {
		fmt.Fprintln(a.Stderr, "[igdev] the REST index is the embedded fallback (method, path, module); start the Gateway and run igdev lookup --refresh for the full one")
	}
}

func (a *App) printLookupIndexes(idx lookupIndexes) {
	w := a.Stdout
	if f := idx.Functions; f != nil {
		fmt.Fprintf(w, "functions: %d (%d documented) from %s, cached at %s\n", f.Functions, f.Documented, f.Image, f.Path)
		for _, m := range f.PrivateModules {
			fmt.Fprintf(w, "  private module %s: %d functions\n", m.Artifact, m.Functions)
		}
	}
	if r := idx.REST; r != nil {
		fmt.Fprintf(w, "rest: %d endpoints from %s, cached at %s\n", r.Endpoints, r.Source, r.Path)
	}
	a.printLookupNotes(idx)
}

// lookupImage picks the image the function index is read from. Every image igdev
// builds for a Gateway is FROM the official one and leaves its jars as they are,
// so any of them will do: this Instance's own image first, then the official
// image, then any other Instance's image of the same version. Only a machine
// with none of them is a fault.
func lookupImage(found project.Found, version string) (string, string, error) {
	var candidates []string
	if found.InProject() {
		if stamp, ok := gate.Decode(found.SetupRaw); ok {
			candidates = append(candidates, stamp.Namespace()+":"+version)
		}
	}
	candidates = append(candidates, lookup.Image(version))
	others, err := lookup.InstanceImages(version)
	if err != nil {
		return "", "", imageFault(version, err)
	}
	candidates = append(candidates, others...)
	for _, image := range candidates {
		id, err := lookup.ImageID(image)
		switch {
		case err == nil:
			return image, id, nil
		case !errors.Is(err, lookup.ErrImageMissing):
			return "", "", imageFault(version, err)
		}
	}
	return "", "", imageFault(version, lookup.ErrImageMissing)
}

// imageFault maps a failure to read an image onto the contract. A machine with
// no Ignition image of the version gets one by starting a Gateway, or by
// pulling the official image.
func imageFault(version string, err error) *contract.Fault {
	image := lookup.Image(version)
	if errors.Is(err, lookup.ErrImageMissing) {
		return contract.NewFault(contract.CodeDocker, contract.ExitFailure,
			fmt.Sprintf("no Ignition %s image is on this machine (neither an igdev Gateway image nor %s), so the function documentation cannot be indexed", version, image)).
			WithCause(err).
			WithRemediation(
				contract.Remediation{Command: "igdev gateway ensure", Why: "build this Instance's Gateway image, which carries the jars"},
				contract.Remediation{Command: "docker pull " + image, Why: "or pull the official image the Gateway image is built from"})
	}
	var engine *lookup.EngineError
	if errors.As(err, &engine) {
		return docker.Fault(engine.Action, engine.Output, engine.Err)
	}
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
		fmt.Sprintf("cannot index the Ignition %s image: %v", version, err)).WithCause(err)
}

func cacheFault(path string, err error) *contract.Fault {
	return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
		fmt.Sprintf("cannot write the lookup index %s: %v", path, err)).WithCause(err)
}
