package lookup

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// REST index sources.
const (
	// SourceOpenAPI is an entry read from a running Gateway's /openapi.json.
	SourceOpenAPI = "openapi"
	// SourceEmbedded is an entry from the Core Catalog's REST plane: method, path
	// and module only.
	SourceEmbedded = "embedded"
)

// Endpoint is one REST operation.
type Endpoint struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Params      []Param  `json:"params"`
	// RequestTypes are the content types the request body accepts, in document
	// order; empty when the operation takes no body.
	RequestTypes []string `json:"request_types,omitempty"`
	// Module is the module the embedded catalog says serves the operation; the
	// live document does not say, so a live entry's module is resolved at query
	// time.
	Module string `json:"module,omitempty"`
	Source string `json:"source"`
}

// Name is the endpoint's lookup name, "METHOD /path".
func (e Endpoint) Name() string { return e.Method + " " + e.Path }

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// openAPIDoc is the part of an OpenAPI 3 document the index reads.
type openAPIDoc struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Parameters map[string]openAPIParam `json:"parameters"`
	} `json:"components"`
}

type openAPIParam struct {
	Ref         string `json:"$ref"`
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

type openAPIOperation struct {
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags"`
	Parameters  []openAPIParam `json:"parameters"`
	RequestBody *struct {
		Content map[string]json.RawMessage `json:"content"`
	} `json:"requestBody"`
}

// ParseOpenAPI indexes every operation of an OpenAPI 3 document, sorted by path
// then method.
func ParseOpenAPI(raw []byte) ([]Endpoint, error) {
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the OpenAPI document is not JSON: %w", err)
	}
	if doc.Paths == nil {
		return nil, fmt.Errorf("the OpenAPI document has no paths")
	}
	resolve := func(p openAPIParam) openAPIParam {
		if name, ok := strings.CutPrefix(p.Ref, "#/components/parameters/"); ok {
			if target, found := doc.Components.Parameters[name]; found {
				return target
			}
		}
		return p
	}
	var out []Endpoint
	for path, item := range doc.Paths {
		var shared []openAPIParam
		if raw, ok := item["parameters"]; ok {
			_ = json.Unmarshal(raw, &shared)
		}
		for _, method := range httpMethods {
			rawOp, ok := item[method]
			if !ok {
				continue
			}
			var op openAPIOperation
			if err := json.Unmarshal(rawOp, &op); err != nil {
				return nil, fmt.Errorf("%s %s cannot be read: %w", strings.ToUpper(method), path, err)
			}
			e := Endpoint{
				Method:      strings.ToUpper(method),
				Path:        path,
				Summary:     strings.TrimSpace(op.Summary),
				Description: strings.TrimSpace(op.Description),
				Tags:        op.Tags,
				Params:      []Param{},
				Source:      SourceOpenAPI,
			}
			seen := map[string]bool{}
			for _, p := range append(append([]openAPIParam(nil), op.Parameters...), shared...) {
				p = resolve(p)
				key := p.In + " " + p.Name
				if p.Name == "" || seen[key] {
					continue
				}
				seen[key] = true
				e.Params = append(e.Params, Param{Name: p.Name, In: p.In, Required: p.Required, Description: strings.TrimSpace(p.Description)})
			}
			if op.RequestBody != nil {
				for contentType := range op.RequestBody.Content {
					e.RequestTypes = append(e.RequestTypes, contentType)
				}
				sort.Slice(e.RequestTypes, func(i, j int) bool {
					return contentRank(e.RequestTypes[i]) < contentRank(e.RequestTypes[j]) ||
						contentRank(e.RequestTypes[i]) == contentRank(e.RequestTypes[j]) && e.RequestTypes[i] < e.RequestTypes[j]
				})
			}
			out = append(out, e)
		}
	}
	sortEndpoints(out)
	return out, nil
}

// contentRank puts JSON first: it is what `gateway api` sends by default.
func contentRank(contentType string) int {
	if strings.HasPrefix(contentType, "application/json") {
		return 0
	}
	return 1
}

func sortEndpoints(out []Endpoint) {
	rank := map[string]int{}
	for i, m := range httpMethods {
		rank[strings.ToUpper(m)] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return rank[out[i].Method] < rank[out[j].Method]
	})
}

// EmbeddedEndpoint is a fallback entry from the Core Catalog's REST plane.
func EmbeddedEndpoint(method, path, module string) Endpoint {
	return Endpoint{Method: method, Path: path, Params: []Param{}, Module: module, Source: SourceEmbedded}
}

// OperationDetail is one operation's full contract: its request body schema per
// content type and its responses, with $ref pointers inlined.
type OperationDetail struct {
	RequestBody map[string]any `json:"request_body,omitempty"`
	Responses   map[string]any `json:"responses,omitempty"`
}

// maxRefDepth bounds how deep inlining follows $ref pointers: past it, or on a
// cycle, a pointer is left as it is.
const maxRefDepth = 6

// Detail reads one operation's request and response schemas out of the
// document it was indexed from.
func Detail(raw []byte, method, path string) (OperationDetail, bool) {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return OperationDetail{}, false
	}
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	if op == nil {
		return OperationDetail{}, false
	}
	var out OperationDetail
	if body, ok := op["requestBody"].(map[string]any); ok {
		body, _ = inline(doc, body, 0, nil).(map[string]any)
		if content, ok := body["content"].(map[string]any); ok {
			out.RequestBody = map[string]any{}
			for contentType, media := range content {
				m, _ := media.(map[string]any)
				out.RequestBody[contentType] = m["schema"]
			}
		}
	}
	if responses, ok := op["responses"].(map[string]any); ok {
		out.Responses = map[string]any{}
		for code, response := range responses {
			r, _ := inline(doc, response, 0, nil).(map[string]any)
			entry := map[string]any{"description": r["description"]}
			if content, ok := r["content"].(map[string]any); ok {
				schemas := map[string]any{}
				for contentType, media := range content {
					m, _ := media.(map[string]any)
					schemas[contentType] = m["schema"]
				}
				entry["content"] = schemas
			}
			out.Responses[code] = entry
		}
	}
	return out, true
}

// inline replaces local $ref pointers with what they point at, up to
// maxRefDepth and never through a pointer already being inlined.
func inline(doc map[string]any, node any, depth int, active []string) any {
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && len(v) == 1 {
			if depth >= maxRefDepth || contains(active, ref) {
				return v
			}
			target := pointer(doc, ref)
			if target == nil {
				return v
			}
			return inline(doc, target, depth+1, append(active, ref))
		}
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[k] = inline(doc, child, depth, active)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = inline(doc, child, depth, active)
		}
		return out
	}
	return node
}

// pointer resolves a local JSON pointer such as #/components/schemas/Name.
func pointer(doc map[string]any, ref string) any {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil
	}
	var node any = doc
	for _, part := range strings.Split(rest, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = m[part]
	}
	return node
}
