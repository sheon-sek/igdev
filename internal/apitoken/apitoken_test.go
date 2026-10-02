package apitoken

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateIsNamedAndHashable(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two generated tokens are equal")
	}
	if !strings.HasPrefix(first, Name+":") {
		t.Fatalf("token %q does not start with %s:", first, Name)
	}
	if _, err := Hash(first); err != nil {
		t.Fatalf("a generated token does not hash: %v", err)
	}
}

// The hash is what Ignition stores for a basic token: SHA-256 over the key's
// bytes, not over its text.
func TestHashIsOverTheKeyBytes(t *testing.T) {
	raw := make([]byte, keyBytes)
	for i := range raw {
		raw[i] = byte(i)
	}
	token := Name + ":" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	got, err := Hash(token)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Hash = %q, want %q", got, want)
	}
}

func TestHashRefusesForeignValues(t *testing.T) {
	for _, value := range []string{
		"",
		"igdev",
		"other:" + base64.RawURLEncoding.EncodeToString(make([]byte, keyBytes)),
		"igdev:not base64!",
		"igdev:" + base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	} {
		if _, err := Hash(value); err == nil {
			t.Errorf("Hash(%q) accepted a value that is not an igdev token", value)
		}
	}
}
