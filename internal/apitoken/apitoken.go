// Package apitoken owns the Instance's own Ignition API token: the credential
// igdev uses to administer the Gateway it runs (resetting an expired trial today).
//
// The token is minted at `igdev setup` and kept, like the admin password, in the
// checkout-local tier `.igdev/local.toml` (mode 0600). The Gateway only ever sees
// its hash: the seed `igdev setup` renders into the Instance image carries the
// SHA-256 of the key bytes, which is what Ignition stores for a basic token, so
// neither a rendered file nor an image layer holds the secret (ADR 0007).
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// Name is the API token resource name inside the Gateway, and the prefix of the
// token value: Ignition reads `<name>:<key>` from X-Ignition-API-Token.
const Name = "igdev"

// SecurityLevel is the security level the token holds, a child of Authenticated.
// Ignition refuses to grant the system-generated Roles levels through config, so
// the seed adds this level and lists it next to Administrator in the Gateway's
// read, write and Designer permissions.
const SecurityLevel = "IgdevAdmin"

// keyBytes is how many random bytes a key carries, as Ignition generates them.
const keyBytes = 32

// Header is the request header Ignition reads an API token from.
const Header = "X-Ignition-API-Token"

// Generate returns a fresh token value `igdev:<key>`, where key is 32 random
// bytes in unpadded base64url.
func Generate() (string, error) {
	raw := make([]byte, keyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random API token key: %w", err)
	}
	return Name + ":" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Hash returns what the Gateway stores for a token: the unpadded base64url
// SHA-256 of the key's bytes. A value that is not `igdev:<32-byte key>` is an
// error, so a hand-edited local tier cannot seed a token nobody can present.
func Hash(token string) (string, error) {
	name, key, ok := strings.Cut(token, ":")
	if !ok || name != Name {
		return "", fmt.Errorf("API token is not %s:<key>", Name)
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != keyBytes {
		return "", fmt.Errorf("API token key is not %d bytes of unpadded base64url", keyBytes)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
