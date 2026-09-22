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
	// EnvPort pins the Gateway HTTP port for one run, overriding the pin the
	// checkout-local tier records.
	EnvPort = "IGDEV_GATEWAY_PORT"
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
	// PortKey is the machine-local Gateway HTTP port pin. It is igdev-owned
	// because the Wizard and `igdev setup --gateway-port` record it, and because
	// a pin has to survive the next credential write unchanged (ADR 0003: ports
	// are a property of the machine, never of the tracked contract).
	PortKey = "http_port"
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
		data = merge(existing, credentialKeys(c))
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

// LoadPort reads the machine-local Gateway HTTP port pin out of local.toml
// bytes. Zero means this machine has no pin, which is the normal case: ports are
// allocated by bind probe. Content igdev cannot parse carries no pin either,
// because setup is the repair path for this tier and replaces it; a pin the file
// does state but that is not a port is an error, because silently ignoring a
// value a person wrote would hide the mistake.
func LoadPort(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var file struct {
		Gateway struct {
			HTTPPort *int `toml:"http_port"`
		} `toml:"gateway"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		return 0, nil
	}
	if file.Gateway.HTTPPort == nil {
		return 0, nil
	}
	port := *file.Gateway.HTTPPort
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("http_port = %d is not a port between 1 and 65535", port)
	}
	return port, nil
}

// WritePort records the machine-local Gateway HTTP port pin in an existing
// checkout-local tier, leaving every line igdev does not own exactly as it was,
// and reports whether the file changed. The tier has to be there and has to
// parse: setup writes the credentials into it first, so a missing or broken file
// is a failure rather than something to paper over.
func WritePort(path string, port int) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read local config %s: %w", path, err)
	}
	if !parses(existing) {
		return false, fmt.Errorf("local config %s is not valid TOML", path)
	}
	data := merge(existing, []ownedKey{{key: PortKey, line: portLine(port)}})
	if bytes.Equal(existing, data) {
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

// ownedKey is one [gateway] key igdev owns: the name it writes under and the
// line it writes there.
type ownedKey struct {
	key  string
	line string
}

// credentialKeys are the two keys every setup writes.
func credentialKeys(c Credentials) []ownedKey {
	return []ownedKey{
		{key: usernameKey, line: credentialLine(usernameKey, c.Username)},
		{key: passwordKey, line: credentialLine(passwordKey, c.Password)},
	}
}

// merge rewrites the keys igdev owns inside existing content, leaving every other
// line exactly as it was. A key the file does not carry yet is added to the
// [gateway] table, which is created at the end when it is missing.
func merge(existing []byte, owned []ownedKey) []byte {
	byKey := make(map[string]string, len(owned))
	for _, o := range owned {
		byKey[o.key] = o.line
	}
	lines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	out := make([]string, 0, len(lines)+len(owned))
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
				if replacement, ok := byKey[strings.TrimSpace(key)]; ok {
					line = replacement
					seen[strings.TrimSpace(key)] = true
				}
			}
			out = append(out, line)
			gatewayEnd = len(out)
			continue
		}
		out = append(out, line)
	}

	missing := make([]string, 0, len(owned))
	for _, o := range owned {
		if !seen[o.key] {
			missing = append(missing, o.line)
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

func portLine(port int) string {
	return fmt.Sprintf("%s = %d", PortKey, port)
}

// tomlString renders one TOML string value; the generated password alphabet and
// a username need nothing beyond a quoted basic string.
func tomlString(value string) string { return fmt.Sprintf("%q", value) }
