package gatewayapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/apitoken"
)

const base = "http://127.0.0.1:18070"

// The token goes to this Instance's URL only: a full URL, a scheme-relative
// path, or a backslash trick is refused before a request exists.
func TestNewRequestRefusesAnotherHost(t *testing.T) {
	for _, path := range []string{
		"http://evil.example/data", "data/api/v1/x", "//evil.example/data", `/\evil.example`, "/a\nb", "",
	} {
		if _, err := NewRequest(http.MethodGet, base, path, nil, "tok", nil); err == nil {
			t.Errorf("NewRequest(%q) built a request", path)
		}
	}
	req, err := NewRequest(http.MethodGet, base, "/data/api/v1/x?next=http://evil.example", nil, "tok", nil)
	if err != nil || req.URL.Host != "127.0.0.1:18070" {
		t.Errorf("a query naming another host moved the request: %v %v", req, err)
	}
}

func TestNewRequestCarriesTheTokenAndOrigin(t *testing.T) {
	req, err := NewRequest("post", base+"/", "/data/api/v1/trial", strings.NewReader("{}"), "tok",
		http.Header{"X-Extra": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		apitoken.Header: "tok", "Origin": base, "Referer": base + "/app/home",
		"Accept": "application/json", "Content-Type": "application/json", "X-Extra": "1",
	} {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %s", req.Method)
	}
}

func TestNewRequestRefusesReplacingTheToken(t *testing.T) {
	for _, name := range []string{"x-ignition-api-token", "Origin", "referer", "Host"} {
		if _, err := NewRequest(http.MethodGet, base, "/x", nil, "tok", http.Header{name: {"other"}}); err == nil {
			t.Errorf("header %s replaced an igdev header", name)
		}
	}
}
