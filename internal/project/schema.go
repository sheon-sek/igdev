package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/semver"
)

// The frozen defaults of contract schema v1. They are the same values the five
// tier resolver falls back to, so a contract written by `igdev init` and a
// checkout with no contract resolve the same environment.
const (
	// DefaultJythonVersion is the Jython the compatibility checker targets.
	DefaultJythonVersion = "2.7.4"
	// DefaultEdition is the Ignition module edition igdev starts with.
	DefaultEdition = "standard"
	// DefaultGatewayMemoryMB is the heap a Gateway is asked for.
	DefaultGatewayMemoryMB = 2048
	// DefaultTimezone is the Gateway timezone when the project does not state
	// one; repositories override it for their own data.
	DefaultTimezone = "UTC"
)

// DefaultScanPaths are the sample scan roots `igdev init` writes: the layout an
// Ignition project repository usually keeps its project scripts in.
var DefaultScanPaths = []string{"src/main/python"}

// Digest is the Contract Digest: sha256 over the Project Contract bytes, with
// the algorithm named so a stored stamp stays self-describing.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Doc is a parsed Project Contract. The field order is the frozen section order
// of the file igdev writes.
type Doc struct {
	Schema   int      `toml:"schema"`
	Project  Project  `toml:"project"`
	Tool     Tool     `toml:"tool"`
	Ignition Ignition `toml:"ignition"`
	Modules  Modules  `toml:"modules"`
	Scan     Scan     `toml:"scan"`
	Commands Commands `toml:"commands"`
	Gateway  Gateway  `toml:"gateway"`
}

// Project names the repository the contract belongs to. It is optional: the
// Project Root is the directory, not the name.
type Project struct {
	Name string `toml:"name"`
}

// Tool pins what the project needs from the CLI itself.
type Tool struct {
	// MinVersion is the oldest igdev that may run against this project.
	MinVersion string `toml:"min_version"`
}

// Ignition selects the Ignition runtime and the Jython compatibility target.
type Ignition struct {
	Version       string `toml:"version"`
	JythonVersion string `toml:"jython_version"`
	Edition       string `toml:"edition"`
}

// Modules is the module whitelist the Environment enables.
type Modules struct {
	Enabled []string `toml:"enabled"`
}

// Scan names the directories capability and syntax scanning walk.
type Scan struct {
	Jython       []string `toml:"jython"`
	Capabilities []string `toml:"capabilities"`
}

// Commands are the project-owned stages igdev dispatches. An empty stage means
// the project declares none.
type Commands struct {
	Check string `toml:"check"`
	Test  string `toml:"test"`
	Build string `toml:"build"`
	Smoke string `toml:"smoke"`
}

// Gateway is the requested Gateway shape. Ports are deliberately absent: they
// are a property of the machine, allocated per Instance at setup (ADR 0003).
type Gateway struct {
	MemoryMB int    `toml:"memory_mb"`
	Timezone string `toml:"timezone"`
	// SmokeEndpoints are the request paths `igdev gateway smoke` checks beyond the
	// root document, in the order they are checked. The key is optional: a
	// contract that does not state it checks the root alone.
	SmokeEndpoints []string `toml:"smoke_endpoints"`
}

// Empty reports whether every stage is undeclared.
func (c Commands) Empty() bool {
	return c.Check == "" && c.Test == "" && c.Build == "" && c.Smoke == ""
}

// DefaultDoc is the contract `igdev init` writes when no flag and no existing
// contract supplies a value.
func DefaultDoc() Doc {
	return Doc{
		Schema: LatestSchema,
		Ignition: Ignition{
			Version:       config.IgnitionTarget,
			JythonVersion: DefaultJythonVersion,
			Edition:       DefaultEdition,
		},
		Modules: Modules{Enabled: []string{}},
		Scan: Scan{
			Jython:       append([]string(nil), DefaultScanPaths...),
			Capabilities: append([]string(nil), DefaultScanPaths...),
		},
		Gateway: Gateway{MemoryMB: DefaultGatewayMemoryMB, Timezone: DefaultTimezone},
	}
}

// ParseDoc decodes and validates a Project Contract. Schema v1 is a closed
// layout: a key igdev does not know is a contract igdev cannot honor, so it is
// refused rather than ignored. Every failure is IGDEV_E_CONFIG_INVALID, the code
// for a tier igdev read for itself and could not interpret.
func ParseDoc(raw []byte, path string) (Doc, error) {
	var doc Doc
	md, err := toml.Decode(string(raw), &doc)
	if err != nil {
		return Doc{}, contractInvalid("project contract %s is not valid TOML: %v", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		names := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			names = append(names, key.String())
		}
		return Doc{}, contractInvalid("project contract %s carries %s igdev does not know; schema %d has a fixed layout",
			path, strings.Join(names, ", "), LatestSchema)
	}
	if err := doc.Validate(path); err != nil {
		return Doc{}, err
	}
	return doc, nil
}

// DecodeDoc decodes a contract without enforcing the closed layout or the value
// rules. `igdev init` uses it to carry a hand-written file's values forward: the
// command is the repair path, and the diff it prints is the review surface.
func DecodeDoc(raw []byte) (Doc, error) {
	var doc Doc
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return Doc{}, contractInvalid("project contract is not valid TOML: %v", err)
	}
	return doc, nil
}

// Filled returns d with every value it leaves unset taken from defaults. It is
// how `igdev init` merges: what the file states wins, what it omits falls back to
// the schema default, and a flag applied afterwards wins over both.
func (d Doc) Filled(defaults Doc) Doc {
	out := d
	if out.Schema == 0 {
		out.Schema = defaults.Schema
	}
	if out.Project.Name == "" {
		out.Project.Name = defaults.Project.Name
	}
	if out.Tool.MinVersion == "" {
		out.Tool.MinVersion = defaults.Tool.MinVersion
	}
	if out.Ignition.Version == "" {
		out.Ignition.Version = defaults.Ignition.Version
	}
	if out.Ignition.JythonVersion == "" {
		out.Ignition.JythonVersion = defaults.Ignition.JythonVersion
	}
	if out.Ignition.Edition == "" {
		out.Ignition.Edition = defaults.Ignition.Edition
	}
	if out.Modules.Enabled == nil {
		out.Modules.Enabled = append([]string(nil), defaults.Modules.Enabled...)
	}
	if out.Scan.Jython == nil {
		out.Scan.Jython = append([]string(nil), defaults.Scan.Jython...)
	}
	if out.Scan.Capabilities == nil {
		out.Scan.Capabilities = append([]string(nil), defaults.Scan.Capabilities...)
	}
	if out.Gateway.MemoryMB == 0 {
		out.Gateway.MemoryMB = defaults.Gateway.MemoryMB
	}
	if out.Gateway.Timezone == "" {
		out.Gateway.Timezone = defaults.Gateway.Timezone
	}
	return out
}

// Validate checks the values a contract states. Structural failure (a key in
// the wrong place, invalid TOML) is ParseDoc's job; this is the value check, and
// it is what makes "version-like string" a rule instead of a convention.
//
// A field left unset is not an error: the embedded defaults cover it, exactly as
// the five-tier resolver does. Only what the file says is checked, so a minimal
// `schema = 1` contract stays valid while a wrong value never does.
//
// An empty path drops the file from the message, which is how `igdev init` blames
// a value the caller typed rather than a file they did not write.
func (d Doc) Validate(path string) error {
	where := ""
	if path != "" {
		where = fmt.Sprintf("project contract %s: ", path)
	}
	for _, field := range []struct {
		key   string
		value string
		check func(string) bool
		what  string
	}{
		{"ignition.version", d.Ignition.Version, versionLike, "a version like 8.3.8"},
		{"ignition.jython_version", d.Ignition.JythonVersion, versionLike, "a version like 2.7.4"},
		{"ignition.edition", d.Ignition.Edition, editionLike, "a lowercase edition name like standard"},
		{"tool.min_version", d.Tool.MinVersion, versionLike, "a version like 0.1.0"},
		{"gateway.timezone", d.Gateway.Timezone, timezoneLike, "an IANA timezone like UTC or Asia/Kuala_Lumpur"},
	} {
		if field.value == "" {
			continue
		}
		if !field.check(field.value) {
			return contractInvalid("%s%s = %q is not %s",
				where, field.key, field.value, field.what)
		}
	}
	for _, name := range d.Modules.Enabled {
		if !moduleLike(name) {
			return contractInvalid("%smodules.enabled entry %q is not a module id", where, name)
		}
	}
	for _, group := range []struct {
		key   string
		paths []string
	}{
		{"scan.jython", d.Scan.Jython},
		{"scan.capabilities", d.Scan.Capabilities},
	} {
		for _, p := range group.paths {
			if strings.TrimSpace(p) == "" {
				return contractInvalid("%s%s carries an empty path", where, group.key)
			}
		}
	}
	for _, endpoint := range d.Gateway.SmokeEndpoints {
		if !smokePath(endpoint) {
			return contractInvalid("%sgateway.smoke_endpoints entry %q is not a request path like /web/home",
				where, endpoint)
		}
	}
	if d.Gateway.MemoryMB < 0 {
		return contractInvalid("%sgateway.memory_mb = %d, want a positive heap in MiB",
			where, d.Gateway.MemoryMB)
	}
	if d.Project.Name != "" && !printable(d.Project.Name) {
		return contractInvalid("%sproject.name carries a control character", where)
	}
	return nil
}

// Render is the canonical schema v1 serialization. igdev is the only writer of a
// Project Contract, so this text is what `igdev init` writes, what a diff shows,
// and what a hand-edited file is compared against.
func (d Doc) Render() []byte {
	var b strings.Builder
	b.WriteString("# igdev Project Contract (schema " + strconv.Itoa(LatestSchema) + ").\n")
	b.WriteString("#\n")
	b.WriteString("# Written by `igdev init`; every igdev write prints a unified diff, so tracked\n")
	b.WriteString("# changes stay reviewable. Hand-editing this file makes the Checkout Setup stale:\n")
	b.WriteString("# run `igdev setup` afterwards.\n")
	fmt.Fprintf(&b, "\nschema = %d\n", d.Schema)

	if d.Project.Name != "" {
		b.WriteString("\n[project]\n")
		fmt.Fprintf(&b, "name = %s\n", tomlString(d.Project.Name))
	}
	if d.Tool.MinVersion != "" {
		b.WriteString("\n[tool]\n")
		fmt.Fprintf(&b, "min_version = %s\n", tomlString(d.Tool.MinVersion))
	}
	b.WriteString("\n[ignition]\n")
	fmt.Fprintf(&b, "version = %s\n", tomlString(d.Ignition.Version))
	fmt.Fprintf(&b, "jython_version = %s\n", tomlString(d.Ignition.JythonVersion))
	fmt.Fprintf(&b, "edition = %s\n", tomlString(d.Ignition.Edition))

	b.WriteString("\n[modules]\n")
	fmt.Fprintf(&b, "enabled = %s\n", tomlStrings(d.Modules.Enabled))

	b.WriteString("\n[scan]\n")
	fmt.Fprintf(&b, "jython = %s\n", tomlStrings(d.Scan.Jython))
	fmt.Fprintf(&b, "capabilities = %s\n", tomlStrings(d.Scan.Capabilities))

	if !d.Commands.Empty() {
		b.WriteString("\n[commands]\n")
		for _, stage := range []struct{ key, value string }{
			{"check", d.Commands.Check},
			{"test", d.Commands.Test},
			{"build", d.Commands.Build},
			{"smoke", d.Commands.Smoke},
		} {
			if stage.value != "" {
				fmt.Fprintf(&b, "%s = %s\n", stage.key, tomlString(stage.value))
			}
		}
	}

	b.WriteString("\n[gateway]\n")
	fmt.Fprintf(&b, "memory_mb = %d\n", d.Gateway.MemoryMB)
	fmt.Fprintf(&b, "timezone = %s\n", tomlString(d.Gateway.Timezone))
	if len(d.Gateway.SmokeEndpoints) > 0 {
		fmt.Fprintf(&b, "smoke_endpoints = %s\n", tomlStrings(d.Gateway.SmokeEndpoints))
	}
	return []byte(b.String())
}

// tomlString renders a one-line TOML basic string. Every value that reaches it
// has already passed validation, so the escaping Go emits stays inside TOML's
// grammar.
func tomlString(s string) string { return strconv.Quote(s) }

func tomlStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, tomlString(v))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

var (
	editionPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	modulePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	timezonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]*$`)
)

func versionLike(s string) bool {
	_, err := semver.Parse(s)
	return err == nil
}

func editionLike(s string) bool { return editionPattern.MatchString(s) }

func moduleLike(s string) bool { return modulePattern.MatchString(s) }

func timezoneLike(s string) bool { return timezonePattern.MatchString(s) }

// smokePath reports a request path smoke can concatenate onto the Gateway URL:
// rooted at "/" and free of whitespace.
func smokePath(s string) bool {
	return strings.HasPrefix(s, "/") && !strings.ContainsAny(s, " \t\r") && printable(s)
}

// printable reports a value that survives a single TOML line unchanged.
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// contractInvalid is the fault for a Project Contract igdev cannot interpret.
// The remediation is the repair path: init rewrites the file, and the diff it
// prints is the review surface.
func contractInvalid(format string, args ...any) *contract.Fault {
	return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure, fmt.Sprintf(format, args...)).
		WithRemediation(contract.Remediation{
			Command: "igdev init",
			Why:     "rewrite igdev.toml at the supported schema layout (the diff shows what changes)",
		})
}
