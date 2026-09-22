// Package localconfig owns the checkout-local tier `.igdev/local.toml`: the
// settings a checkout needs on this machine that the tracked Project Contract
// must never hold — today the Gateway admin credentials.
//
// The file is mode 0600 inside the gitignored Checkout Setup, and its contents
// are never rendered into output. Ports and secrets are machine-local by design
// (ADR 0003), which is why they live here and not in `igdev.toml`. igdev owns
// only the two credential keys: any other line the file carries — a comment, a
// port pin a person wrote — is preserved across a write.
package localconfig

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sheon-sek/igdev/internal/atomicfile"
)

// The frozen environment overrides. They are deliberately not part of the
// five-tier config schema: a resolved value is reported by `igdev status --json`,
// and a password must never surface there.
const (
	// EnvUsername overrides the Gateway admin username.
	EnvUsername = "IGDEV_GATEWAY_ADMIN_USERNAME"
	// EnvPassword overrides the generated Gateway admin password.
	EnvPassword = "IGDEV_GATEWAY_ADMIN_PASSWORD"
)

// DefaultUsername is the Gateway admin user setup creates when nothing overrides
// it.
const DefaultUsername = "admin"

// PasswordLength is how many characters a generated admin password carries.
const PasswordLength = 24

// Mode is the permission bits the file is written with: the password inside is
// readable by its owner alone.
const Mode os.FileMode = 0o600

// gatewaySection is the table the credentials live in.
const gatewaySection = "gateway"

// The credential keys inside that table.
const (
	usernameKey = "admin_username"
	passwordKey = "admin_password"
)

// passwordAlphabet omits the characters people misread (0/O, 1/l/I) and every
// character that would need escaping inside a TOML string or a shell word.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Credentials are the Gateway admin credentials recorded for one checkout.
type Credentials struct {
	Username string `toml:"admin_username" json:"username"`
	Password string `toml:"admin_password" json:"-"`
}

// GeneratePassword returns a cryptographically random password drawn uniformly
// from the printable, quote-safe alphabet.
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	var b strings.Builder
	b.Grow(PasswordLength)
	for range PasswordLength {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("read random password: %w", err)
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// Load reads the credentials out of local.toml bytes. Values the file does not
// state come back empty, which is how setup decides whether it has to generate a
// password. A file that cannot be parsed is reported as an error, so setup can
// tell "no credentials yet" from "this tier is broken".
func Load(raw []byte) (Credentials, error) {
	if len(raw) == 0 {
		return Credentials{}, nil
	}
	var file struct {
		Gateway Credentials `toml:"gateway"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		return Credentials{}, fmt.Errorf("local config is not valid TOML: %w", err)
	}
	return file.Gateway, nil
}

// Render serializes a fresh checkout-local tier carrying the credentials. Like
// every igdev file it is generated, disposable, and byte-identical for identical
// inputs.
func Render(c Credentials) []byte {
	var b strings.Builder
	b.WriteString("# igdev checkout-local config: this checkout, this machine.\n")
	b.WriteString("#\n")
	b.WriteString("# Mode 0600 inside the gitignored Checkout Setup. The tracked Project Contract\n")
	b.WriteString("# never holds secrets or ports (ADR 0003), so igdev records them here instead of\n")
	b.WriteString("# in the repository, and never prints the password. `igdev gateway credentials\n")
	b.WriteString("# --json` (ticket 11) is the only way to read it back out.\n")
	b.WriteString("\n[" + gatewaySection + "]\n")
	fmt.Fprintf(&b, "%s = %s\n", usernameKey, tomlString(c.Username))
	fmt.Fprintf(&b, "%s = %s\n", passwordKey, tomlString(c.Password))
	return []byte(b.String())
}

// Write stores the tier at path with mode 0600, creating .igdev/ when the
// checkout has never been set up, and reports whether anything changed. Existing
// content that parses keeps every line igdev does not own; content that does not
// parse is replaced, because a broken tier would otherwise wedge every command
// that reads it.
func Write(path string, c Credentials) (bool, error) {
	existing, readErr := os.ReadFile(path)
	var data []byte
	switch {
	case readErr != nil || !parses(existing):
		data = Render(c)
	default:
		data = merge(existing, c)
		if !parses(data) {
			// A shape the line-level merge cannot patch — an inline `gateway = {…}`
			// table, a quoted table name — would otherwise be left duplicated and
			// unreadable, wedging every command that resolves config.
			data = Render(c)
		}
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() == Mode.Perm() && bytes.Equal(existing, data) {
		return false, nil
	}
	if err := atomicfile.Write(path, data, Mode, 0o700); err != nil {
		return false, fmt.Errorf("write local config %s: %w", path, err)
	}
	return true, nil
}

func parses(raw []byte) bool {
	var probe map[string]any
	return toml.Unmarshal(raw, &probe) == nil
}

// merge rewrites the two credential keys inside existing content, leaving every
// other line exactly as it was. A key the file does not carry yet is added to
// the [gateway] table, which is created at the end when it is missing.
func merge(existing []byte, c Credentials) []byte {
	lines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	out := make([]string, 0, len(lines)+3)
	section := ""
	// gatewayEnd is the index in out just past the last line of the [gateway]
	// table, where a key the file is missing has to be inserted.
	gatewayEnd := -1
	seen := map[string]bool{}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
			section = strings.Trim(trimmed, "[]")
			out = append(out, line)
			if section == gatewaySection {
				gatewayEnd = len(out)
			}
			continue
		case section == gatewaySection:
			key, _, hasValue := strings.Cut(trimmed, "=")
			if hasValue {
				switch strings.TrimSpace(key) {
				case usernameKey:
					line, seen[usernameKey] = credentialLine(usernameKey, c.Username), true
				case passwordKey:
					line, seen[passwordKey] = credentialLine(passwordKey, c.Password), true
				}
			}
			out = append(out, line)
			gatewayEnd = len(out)
			continue
		}
		out = append(out, line)
	}

	missing := make([]string, 0, 2)
	for _, pair := range []struct{ key, value string }{
		{usernameKey, c.Username},
		{passwordKey, c.Password},
	} {
		if !seen[pair.key] {
			missing = append(missing, credentialLine(pair.key, pair.value))
		}
	}
	switch {
	case len(missing) == 0:
	case gatewayEnd >= 0:
		merged := make([]string, 0, len(out)+len(missing))
		merged = append(merged, out[:gatewayEnd]...)
		merged = append(merged, missing...)
		merged = append(merged, out[gatewayEnd:]...)
		out = merged
	default:
		out = append(out, "", "["+gatewaySection+"]")
		out = append(out, missing...)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func credentialLine(key, value string) string {
	return fmt.Sprintf("%s = %s", key, tomlString(value))
}

// tomlString renders one TOML string value; the generated password alphabet and
// a username need nothing beyond a quoted basic string.
func tomlString(value string) string { return fmt.Sprintf("%q", value) }
