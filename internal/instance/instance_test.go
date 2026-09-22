package instance

import (
	"strings"
	"testing"
)

// An Instance identity is a fresh random UUID every time: two checkouts set up
// seconds apart must not share one.
func TestNewIDIsRandomAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !Valid(id) {
			t.Fatalf("NewID produced %q, which Valid rejects", id)
		}
		if id[14] != '4' {
			t.Errorf("id %q is not version 4 (version nibble %q)", id, id[14])
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

// The namespace is derived from the UUID alone, so it is stable for an Instance
// and unrelated to where the checkout lives.
func TestNamespaceUsesTheFirstEightHexDigits(t *testing.T) {
	const id = "3b1f0c2a-9d4e-4f6b-8a11-0c2d3e4f5a6b"
	if got := Namespace(id); got != "igdev-3b1f0c2a" {
		t.Errorf("Namespace(%q) = %q, want igdev-3b1f0c2a", id, got)
	}
	if got := Short(id); got != "3b1f0c2a" {
		t.Errorf("Short(%q) = %q, want 3b1f0c2a", id, got)
	}
	if !strings.HasPrefix(Namespace(id), NamespacePrefix) {
		t.Errorf("Namespace(%q) does not carry the igdev- prefix", id)
	}
}

// A record another tool wrote is judged by shape: an identity only has to be
// stable and unique, and a malformed one must never be reused as if it were.
func TestValidAcceptsUUIDShapeOnly(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"3b1f0c2a-9d4e-4f6b-8a11-0c2d3e4f5a6b", true},
		{"", false},
		{"not-a-uuid", false},
		{"3b1f0c2a9d4e4f6b8a110c2d3e4f5a6b", false},
		{"3B1F0C2A-9D4E-4F6B-8A11-0C2D3E4F5A6B", false},
		{"3b1f0c2a-9d4e-4f6b-8a11-0c2d3e4f5a6", false},
	} {
		if got := Valid(tc.id); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
