// Package gatewayapi builds the requests igdev sends to an Instance's Gateway
// REST API with the Instance's own API token (ADR 0007).
//
// The token may only ever reach the Instance it belongs to, so a request is built
// from the recorded base URL and a path, never from a URL the caller supplies,
// and the headers that carry the token and the request's origin cannot be
// replaced by the caller.
package gatewayapi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sheon-sek/igdev/internal/apitoken"
)

// reserved are the headers igdev sets itself and a caller cannot replace.
var reserved = []string{apitoken.Header, "Origin", "Referer", "Host"}

// Reserved reports whether a header name is one igdev sets itself.
func Reserved(name string) bool {
	for _, r := range reserved {
		if strings.EqualFold(strings.TrimSpace(name), r) {
			return true
		}
	}
	return false
}

// PathProblem says why path cannot be sent to the Instance, or "" when it can.
// A path is absolute on the Gateway, with no scheme and no host of its own.
func PathProblem(path string) string {
	switch {
	case !strings.HasPrefix(path, "/"):
		return "must start with /; a full URL or another host is never sent the Instance token"
	case strings.HasPrefix(path, "//"), strings.Contains(path, `\`):
		return "names a host of its own; only a path on this Instance's Gateway is accepted"
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return "carries a control character"
		}
	}
	return ""
}

// NewRequest builds one request to base+path with the token and the origin
// headers the Gateway's own web UI sends, then extra headers on top. A reserved
// header in extra, a bad path, or a URL whose host is not base's is an error.
func NewRequest(method, base, path string, body io.Reader, token string, extra http.Header) (*http.Request, error) {
	if reason := PathProblem(path); reason != "" {
		return nil, fmt.Errorf("path %q %s", path, reason)
	}
	base = strings.TrimRight(base, "/")
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("the recorded Gateway URL %q is not a URL: %w", base, err)
	}
	target, err := url.Parse(base + path)
	if err != nil {
		return nil, fmt.Errorf("path %q is not a valid request path: %w", path, err)
	}
	if target.Scheme != baseURL.Scheme || target.Host != baseURL.Host {
		return nil, fmt.Errorf("path %q resolves to %s, not this Instance's Gateway", path, target.Host)
	}
	req, err := http.NewRequest(strings.ToUpper(method), target.String(), body)
	if err != nil {
		return nil, err
	}
	for name, values := range extra {
		if Reserved(name) {
			return nil, fmt.Errorf("header %s is set by igdev and cannot be replaced", name)
		}
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", base+"/app/home")
	if token != "" {
		req.Header.Set(apitoken.Header, token)
	}
	return req, nil
}
