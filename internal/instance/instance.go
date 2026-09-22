// Package instance mints and names the identity of one Instance: the pairing of
// a Project Contract with a Checkout Setup. The identity is a random UUID minted
// at first setup, so it never depends on the checkout path and a moved directory
// keeps its Instance (ADR 0003).
package instance

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

// NamespacePrefix starts every Docker resource name igdev owns, so an Instance's
// Objects are recognisable and collapsible.
const NamespacePrefix = "igdev-"

// ShortLength is how many hex characters of the UUID name an Instance's
// namespace: the first 8 hex digits of the UUID, dashes removed.
const ShortLength = 8

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NewID mints a version 4 (random) UUID from crypto/rand.
func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random instance id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// Valid reports whether id has the canonical UUID text shape. A record written by
// another tool is accepted on shape alone: the identity only has to be stable and
// unique, not freshly minted here.
func Valid(id string) bool { return idPattern.MatchString(id) }

// Short returns the first ShortLength hex characters of id with the dashes
// removed. An id too short to hold them is returned as its hex digits, so a
// malformed record still yields a usable (if useless) namespace.
func Short(id string) string {
	hex := strings.ReplaceAll(strings.ToLower(id), "-", "")
	if len(hex) < ShortLength {
		return hex
	}
	return hex[:ShortLength]
}

// Namespace is the Docker namespace of the Instance identified by id: the compose
// project name, the gateway container name, and the image prefix.
func Namespace(id string) string { return NamespacePrefix + Short(id) }
