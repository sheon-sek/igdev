package lookup

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Kinds a result can have.
const (
	KindFunction = "function"
	KindREST     = "rest"
)

// Hit is one search result. Exactly one of Function and Endpoint is set.
type Hit struct {
	Kind     string
	Name     string
	Score    float64
	Function *Function
	Endpoint *Endpoint
}

// The weights of a query term by where it matched. A term counts once, at the
// best place it matched, so a long description cannot outrank a name.
const (
	weightLeaf        = 3.0
	weightName        = 2.5
	weightSummary     = 1.5
	weightDescription = 1.0
	weightMethod      = 1.0
	bonusAllTerms     = 1.0
	bonusExactName    = 10.0
	penaltyDeprecated = 0.5
)

// document is one searchable entry, its text split into stemmed terms.
type document struct {
	hit         Hit
	leaf        map[string]bool
	name        map[string]bool
	summary     map[string]bool
	description map[string]bool
	methodWords map[string]bool
	deprecated  bool
}

// methodWords are the query words that mean an HTTP method.
var methodWords = map[string][]string{
	"GET":    {"get", "read", "list", "fetch", "show", "query", "find", "browse", "export", "download"},
	"POST":   {"create", "add", "import", "upload", "run", "start", "send", "make", "new", "execute"},
	"PUT":    {"update", "set", "replace", "change", "edit", "modify", "write"},
	"PATCH":  {"update", "patch", "change", "edit", "modify"},
	"DELETE": {"delete", "remove", "drop", "clear", "uninstall"},
}

// Search ranks functions and endpoints against a free-text query. kind narrows
// the search to one kind when it is not empty. The best limit hits are returned,
// highest score first; a tie goes to the shorter name.
func Search(functions []Function, endpoints []Endpoint, query, kind string, limit int) []Hit {
	terms := uniqueTerms(query)
	exact := strings.TrimSpace(query)
	if len(terms) == 0 {
		return nil
	}
	var docs []document
	if kind == "" || kind == KindFunction {
		for i := range functions {
			docs = append(docs, functionDocument(&functions[i]))
		}
	}
	if kind == "" || kind == KindREST {
		for i := range endpoints {
			docs = append(docs, endpointDocument(&endpoints[i]))
		}
	}
	var hits []Hit
	for _, d := range docs {
		score, matched := 0.0, 0
		for _, term := range terms {
			best := 0.0
			switch {
			case d.leaf[term]:
				best = weightLeaf
			case d.name[term]:
				best = weightName
			case d.summary[term]:
				best = weightSummary
			case d.description[term]:
				best = weightDescription
			}
			if d.methodWords[term] {
				best += weightMethod
			}
			if best > 0 {
				matched++
			}
			score += best
		}
		if matched == 0 {
			continue
		}
		if matched == len(terms) {
			score += bonusAllTerms
		}
		if strings.EqualFold(exact, d.hit.Name) {
			score += bonusExactName
		}
		if d.deprecated {
			score -= penaltyDeprecated
		}
		hit := d.hit
		hit.Score = math.Round(score*100) / 100
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if len(hits[i].Name) != len(hits[j].Name) {
			return len(hits[i].Name) < len(hits[j].Name)
		}
		return hits[i].Name < hits[j].Name
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func functionDocument(f *Function) document {
	leaf, namespace := f.Name, ""
	if i := strings.LastIndex(f.Name, "."); i >= 0 {
		namespace, leaf = f.Name[:i], f.Name[i+1:]
	}
	return document{
		hit:         Hit{Kind: KindFunction, Name: f.Name, Function: f},
		leaf:        termSet(leaf),
		name:        termSet(namespace),
		summary:     termSet(firstSentence(f.Description)),
		description: termSet(f.Description + " " + f.Returns + " " + paramText(f.Params)),
		deprecated:  f.Deprecated != "",
	}
}

func endpointDocument(e *Endpoint) document {
	var literal []string
	for _, segment := range strings.Split(e.Path, "/") {
		if !strings.HasPrefix(segment, "{") {
			literal = append(literal, segment)
		}
	}
	// The last literal segments say what the operation is about; the rest is the
	// route prefix every operation of a module shares.
	leafStart := len(literal) - 2
	if leafStart < 0 {
		leafStart = 0
	}
	methods := map[string]bool{}
	for _, word := range methodWords[e.Method] {
		methods[stem(word)] = true
	}
	return document{
		hit:         Hit{Kind: KindREST, Name: e.Name(), Endpoint: e},
		leaf:        termSet(strings.Join(literal[leafStart:], " ")),
		name:        termSet(strings.Join(literal, " ")),
		summary:     termSet(e.Summary + " " + strings.Join(e.Tags, " ")),
		description: termSet(e.Description + " " + paramText(e.Params)),
		methodWords: methods,
	}
}

func paramText(params []Param) string {
	var b strings.Builder
	for _, p := range params {
		b.WriteString(p.Name)
		b.WriteByte(' ')
	}
	return b.String()
}

func firstSentence(text string) string {
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i]
	}
	return text
}

// stopWords carry no meaning in a query.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "for": true, "in": true, "on": true,
	"and": true, "or": true, "by": true, "with": true, "from": true, "is": true, "are": true,
	"how": true, "do": true, "i": true, "me": true, "my": true, "this": true, "that": true,
	"it": true, "into": true, "at": true, "as": true, "be": true, "can": true, "what": true,
	"which": true, "system": true, "data": true, "api": true, "v1": true,
}

func uniqueTerms(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range terms(text) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func termSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range terms(text) {
		out[t] = true
	}
	return out
}

// terms splits text into lower-case, stemmed words: at every character that is
// not a letter or digit, and inside camelCase and ACRONYMWords.
func terms(text string) []string {
	var out []string
	for _, word := range splitWords(text) {
		w := strings.ToLower(word)
		if stopWords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

func splitWords(text string) []string {
	var out []string
	runes := []rune(text)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			out = append(out, string(runes[start:end]))
		}
		start = -1
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := runes[i-1]
		lowerToUpper := unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev))
		acronymEnd := unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if lowerToUpper || acronymEnd {
			flush(i)
			start = i
		}
	}
	flush(len(runes))
	return out
}

// stem folds the common English inflections, so "reads", "reading" and "read"
// are one term. It is deliberately small: the index is names and short
// sentences, and an over-eager stemmer merges words that differ.
func stem(w string) string {
	w = stripSuffix(w)
	if len(w) > 4 && strings.HasSuffix(w, "e") {
		return w[:len(w)-1]
	}
	return w
}

func stripSuffix(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 5 && strings.HasSuffix(w, "ing"):
		return w[:len(w)-3]
	case len(w) > 4 && (strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	case len(w) > 4 && strings.HasSuffix(w, "ed") && !strings.HasSuffix(w, "eed"):
		return w[:len(w)-2]
	}
	return w
}
