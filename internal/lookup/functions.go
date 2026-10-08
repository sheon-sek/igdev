package lookup

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed assets/namespaces.tsv
var namespaceTable string

// namespaces is the embedded bundle-to-namespace table, and aliases the second
// names some functions are also registered under, keyed by bundle.function.
var namespaces, aliases = func() (map[string]string, map[string][]string) {
	out, alias := map[string]string{}, map[string][]string{}
	for _, line := range strings.Split(namespaceTable, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, name, ok := strings.Cut(line, "\t")
		key, name = strings.TrimSpace(key), strings.TrimSpace(name)
		switch {
		case !ok:
		case strings.Contains(key, "."):
			alias[key] = append(alias[key], name)
		default:
			out[key] = name
		}
	}
	return out, alias
}()

// Param is one parameter of a function or an endpoint.
type Param struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Default and Optional are a function parameter's bundle annotations.
	Default  string `json:"default,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	// In and Required describe an endpoint parameter: path, query or header.
	In       string `json:"in,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Function is one documented scripting function.
type Function struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	Description string   `json:"description"`
	Params      []Param  `json:"params"`
	Returns     string   `json:"returns,omitempty"`
	Deprecated  string   `json:"deprecated,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
	// Module is the id of the module whose archive documents the function, or
	// "platform" for the image's own jars.
	Module string `json:"module"`
	// Source is the bundle the entry was read from, or "catalog" for a function
	// the catalog knows and no bundle documents.
	Source string `json:"source"`
	// Note says what is uncertain about the entry, such as a namespace no table
	// or catalog row could name.
	Note string `json:"note,omitempty"`
}

// Platform is the module of a function the image's own jars carry.
const Platform = "platform"

// buildFunctions turns bundles into functions. known lists the function names
// the Effective Catalog carries; it names the namespace of a bundle the table
// does not, and moves a function to the namespace that really has it when its
// bundle mixes two. keepUnmapped keeps a bundle no table or vote can place,
// under its class name: a private module documents its functions without saying
// where they are registered.
func buildFunctions(bundles []bundle, known []string, keepUnmapped bool) []Function {
	byLeaf := map[string][]string{}
	knownSet := map[string]bool{}
	for _, name := range known {
		knownSet[name] = true
		if i := strings.LastIndex(name, "."); i > 0 {
			byLeaf[name[i+1:]] = append(byLeaf[name[i+1:]], name[:i])
		}
	}
	merged := map[string]*Function{}
	var order []string
	for _, b := range bundles {
		fns := b.functions()
		votes := map[string]int{}
		for _, fn := range fns {
			for _, ns := range byLeaf[fn] {
				votes[ns]++
			}
		}
		ns, mapped := namespaces[b.Class]
		if !mapped {
			ns, mapped = majority(votes)
		}
		note := ""
		if !mapped {
			// A bundle with only .desc keys documents the properties of a settings
			// record, not functions: a scripting bundle names parameters or returns.
			if !keepUnmapped || !b.scripting() {
				continue
			}
			ns = b.Class
			note = "the namespace this function is registered under is not known; declare it in the Project Overlay to look it up by its system.* name"
		}
		module := b.Module
		if module == "" {
			module = Platform
		}
		for _, fn := range fns {
			names := append([]string{placeFunction(ns, fn, votes, knownSet)}, aliases[b.Class+"."+fn]...)
			for _, name := range names {
				entry := functionOf(b, fn, name, module, note)
				if existing, ok := merged[name]; ok {
					mergeFunction(existing, entry)
					continue
				}
				merged[name] = &entry
				order = append(order, name)
			}
		}
	}
	sort.Strings(order)
	out := make([]Function, 0, len(order))
	for _, name := range order {
		out = append(out, *merged[name])
	}
	return out
}

// majority is the namespace most of a bundle's function names belong to, when
// at least two do and no other namespace ties it.
func majority(votes map[string]int) (string, bool) {
	best, count, tie := "", 0, false
	for ns, n := range votes {
		switch {
		case n > count:
			best, count, tie = ns, n, false
		case n == count:
			tie = true
		}
	}
	if count < 2 || tie {
		return "", false
	}
	return best, true
}

// placeFunction names one function. A bundle can mix namespaces, as the alarm
// notification module's bundle does with system.alarm and system.roster: when
// the catalog has no ns.fn but exactly one other namespace the bundle votes for
// has fn, the function is that one.
func placeFunction(ns, fn string, votes map[string]int, known map[string]bool) string {
	name := ns + "." + fn
	if known[name] || len(known) == 0 {
		return name
	}
	var alternatives []string
	for other, n := range votes {
		if other != ns && n >= 2 && known[other+"."+fn] {
			alternatives = append(alternatives, other)
		}
	}
	if len(alternatives) == 1 {
		return alternatives[0] + "." + fn
	}
	return name
}

// functionOf reads one function's keys out of its bundle.
func functionOf(b bundle, fn, name, module, note string) Function {
	values := b.props.values
	out := Function{
		Name:        name,
		Scopes:      append([]string(nil), b.Scopes...),
		Description: values[fn+".desc"],
		Params:      []Param{},
		Returns:     values[fn+".returns"],
		Deprecated:  values[fn+".deprecated"],
		Replacement: values[fn+".replacement"],
		Module:      module,
		Source:      b.Source,
		Note:        note,
	}
	prefix := fn + ".param."
	for _, key := range b.props.keys {
		param, ok := strings.CutPrefix(key, prefix)
		if !ok || !validIdentifier(param) {
			continue
		}
		out.Params = append(out.Params, Param{
			Name:        param,
			Description: values[key],
			Default:     values[key+".default"],
			Optional:    strings.EqualFold(values[key+".optional"], "true"),
		})
	}
	return out
}

// mergeFunction folds a second documentation of the same function into the
// first: the scopes are the union, and the fuller text wins, which is how a
// Gateway-only override and the common bundle's version combine.
func mergeFunction(into *Function, other Function) {
	for _, scope := range other.Scopes {
		if !contains(into.Scopes, scope) {
			into.Scopes = append(into.Scopes, scope)
		}
	}
	into.Scopes = orderScopes(into.Scopes)
	if weight(other) > weight(*into) {
		scopes := into.Scopes
		*into = other
		into.Scopes = scopes
	}
}

func weight(f Function) int { return len(f.Params)*1000 + len(f.Description) + len(f.Returns) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// orderScopes sorts scopes into the reporting order.
func orderScopes(scopes []string) []string {
	rank := map[string]int{ScopeGateway: 0, ScopeClient: 1, ScopeDesigner: 2}
	sort.SliceStable(scopes, func(i, j int) bool { return rank[scopes[i]] < rank[scopes[j]] })
	return scopes
}

// ScopeLabel renders scopes for a reader: "all" when a function runs in every
// scope, otherwise the scopes joined by commas.
func ScopeLabel(scopes []string) string {
	if len(scopes) == 3 {
		return "all"
	}
	return strings.Join(orderScopes(append([]string(nil), scopes...)), ",")
}
