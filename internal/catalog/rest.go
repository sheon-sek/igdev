package catalog

import (
	"strings"
)

// Methods are the HTTP methods a REST capability may name, in the order the
// diagnostics list them.
var Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE"}

// validMethod reports whether method is one of the eight accepted methods.
func validMethod(method string) bool {
	for _, m := range Methods {
		if m == method {
			return true
		}
	}
	return false
}

// RESTRequest is a parsed REST capability: an HTTP method and a concrete path,
// or a bare path with no method.
type RESTRequest struct {
	// Method is the upper-case method; empty when the capability named a bare
	// path, which asks about every method.
	Method string
	// Path is the concrete path with any query string removed.
	Path string
}

// ParseRESTRequest parses "METHOD /data/..." or "/data/...". The method is
// case-insensitive, exactly as the bash specification's rest_request_parts is:
// "get /data/api/v1/gateway-info" is the same capability as the upper-case form.
func ParseRESTRequest(capability string) (RESTRequest, bool) {
	c := strings.TrimSpace(capability)
	method := ""
	rest := c
	if i := strings.IndexAny(c, " \t"); i >= 0 {
		candidate := strings.ToUpper(c[:i])
		if !validMethod(candidate) {
			return RESTRequest{}, false
		}
		method = candidate
		rest = strings.TrimSpace(c[i+1:])
	}
	if !strings.HasPrefix(rest, "/data/") || strings.ContainsAny(rest, " \t") {
		return RESTRequest{}, false
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return RESTRequest{}, false
	}
	return RESTRequest{Method: method, Path: rest}, true
}

// restKey is a REST row's identity: the method plus the path with its trailing
// slash removed. Trailing slashes are tolerated, so "/data/x" and "/data/x/" are
// the same key.
func restKey(method, template string) string {
	return method + " " + strings.TrimSuffix(template, "/")
}

// pathMatches reports whether a concrete path matches a catalog template. A
// "{param}" segment matches exactly one concrete segment; a trailing slash is
// tolerated on either side; every other segment must be equal.
func pathMatches(template, concrete string) bool {
	t := strings.Split(strings.TrimSuffix(template, "/"), "/")
	p := strings.Split(strings.TrimSuffix(concrete, "/"), "/")
	if len(t) != len(p) {
		return false
	}
	for i := range t {
		if isParam(t[i]) {
			continue
		}
		if t[i] != p[i] {
			return false
		}
	}
	return true
}

// isParam reports whether a template segment is a "{param}" placeholder.
func isParam(segment string) bool {
	return len(segment) > 2 && strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}
