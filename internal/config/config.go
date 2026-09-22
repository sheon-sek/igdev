// Package config implements igdev's five-level configuration resolver.
//
// Precedence is frozen: CLI flags > IGDEV_* environment > the checkout-local
// file .igdev/local.toml > the Project Contract igdev.toml > embedded defaults.
// Resolution is pure: callers hand in the raw bytes and environment of each
// tier so the same code runs in the binary and in an in-process unit test.
package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Source names the tier a resolved value came from. The strings are part of the
// frozen contract: they appear in `igdev status --json`.
type Source string

const (
	SourceFlag     Source = "flag"
	SourceEnv      Source = "env"
	SourceLocal    Source = "local"
	SourceContract Source = "contract"
	SourceDefault  Source = "default"
)

// Precedence is the frozen order, highest first.
var Precedence = []Source{SourceFlag, SourceEnv, SourceLocal, SourceContract, SourceDefault}

// Kind is the value type of a config key.
type Kind int

const (
	KindString Kind = iota
	KindBool
	KindInt
)

// Key is one tunable with a frozen path, environment name, and default.
type Key struct {
	// Path is the dotted key, used in flags, files, and status output.
	Path string
	// Env is the IGDEV_* variable that sets this key.
	Env  string
	Kind Kind
	// Default is the embedded default value.
	Default any
	// Oneof lists the accepted string values; empty means any string.
	Oneof []string
	// Desc documents the key for help output.
	Desc string
}

// ReleaseAPIEndpoint is the real GitHub Releases endpoint queried for the
// update notice.
const ReleaseAPIEndpoint = "https://api.github.com/repos/sheon-sek/igdev/releases/latest"

// IgnitionTarget is the Ignition version igdev 0.1 knows about.
const IgnitionTarget = "8.3.8"

// Schema is the frozen key table for the CLI Contract Version in force.
var Schema = []Key{
	{
		Path: "output.format", Env: "IGDEV_OUTPUT_FORMAT", Kind: KindString, Default: "text",
		Oneof: []string{"text", "json"},
		Desc:  "Output dialect: text for humans, json for agents (set by --json).",
	},
	{
		Path: "project.ignition_version", Env: "IGDEV_PROJECT_IGNITION_VERSION", Kind: KindString, Default: IgnitionTarget,
		Desc: "Ignition version this checkout targets; selects the Core Catalog.",
	},
	{
		Path: "updater.enabled", Env: "IGDEV_UPDATER_ENABLED", Kind: KindBool, Default: true,
		Desc: "Whether igdev may print the cached update-available notice.",
	},
	{
		Path: "updater.releases_api_url", Env: "IGDEV_UPDATER_RELEASES_API_URL", Kind: KindString, Default: ReleaseAPIEndpoint,
		Desc: "Releases endpoint queried by the update notice.",
	},
	{
		Path: "updater.cache_ttl_hours", Env: "IGDEV_UPDATER_CACHE_TTL_HOURS", Kind: KindInt, Default: 24,
		Desc: "How long a fetched release result is reused.",
	},
	{
		Path: "updater.timeout_seconds", Env: "IGDEV_UPDATER_TIMEOUT_SECONDS", Kind: KindInt, Default: 2,
		Desc: "Hard ceiling on a single release fetch.",
	},
}

func keyByPath(path string) (Key, bool) {
	for _, k := range Schema {
		if k.Path == path {
			return k, true
		}
	}
	return Key{}, false
}

// Value is a resolved setting together with the tier it won.
type Value struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source Source `json:"source"`
}

// Resolution is the outcome of applying the precedence rule to every key.
type Resolution struct {
	values map[string]Value
	// TierFiles records where each file tier was read from, for status output.
	TierFiles map[string]string
}

// Get returns the resolved value for a frozen key path.
func (r *Resolution) Get(path string) Value {
	return r.values[path]
}

// String returns the resolved string value, or "" when absent.
func (r *Resolution) String(path string) string {
	if v, ok := r.Get(path).Value.(string); ok {
		return v
	}
	return ""
}

// Bool returns the resolved bool value, falling back to the embedded default.
func (r *Resolution) Bool(path string) bool {
	if v, ok := r.Get(path).Value.(bool); ok {
		return v
	}
	return false
}

// Int returns the resolved int value, falling back to the embedded default.
func (r *Resolution) Int(path string) int {
	if v, ok := r.Get(path).Value.(int); ok {
		return v
	}
	return 0
}

// Values lists every key with its winning tier, sorted by path. This is what
// makes precedence observable in `igdev status --json`.
func (r *Resolution) Values() []Value {
	out := make([]Value, 0, len(r.values))
	for _, v := range r.values {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// IsJSON reports whether output must be the JSON envelope.
func (r *Resolution) IsJSON() bool {
	return r.String("output.format") == "json"
}

// Input carries the raw bytes of each tier.
type Input struct {
	// Flags holds `--config key=value` pairs plus anything a dedicated flag set.
	Flags map[string]string
	// Environ is the process environment as KEY=VALUE strings.
	Environ []string
	// LocalTOML is the contents of .igdev/local.toml; nil when absent.
	LocalTOML []byte
	// LocalPath is where LocalTOML was read from.
	LocalPath string
	// ContractTOML is the contents of igdev.toml; nil when absent.
	ContractTOML []byte
	// ContractPath is where ContractTOML was read from.
	ContractPath string
}

// Resolve applies the frozen precedence rule. A tier that fails to parse is
// reported as IGDEV_E_CONFIG_INVALID; unknown keys inside a file are ignored so
// the Project Contract can carry sections this resolver does not know.
func Resolve(in Input) (*Resolution, error) {
	tiers := map[Source]map[string]any{}
	env := environMap(in.Environ)

	local, err := parseTOML("local config", in.LocalPath, in.LocalTOML)
	if err != nil {
		return nil, err
	}
	tiers[SourceLocal] = local

	contractTier, err := parseTOML("project contract", in.ContractPath, in.ContractTOML)
	if err != nil {
		return nil, err
	}
	tiers[SourceContract] = contractTier

	flagTier, err := parseFlags(in.Flags)
	if err != nil {
		return nil, err
	}
	tiers[SourceFlag] = flagTier

	envTier, err := parseEnv(Schema, env)
	if err != nil {
		return nil, err
	}
	tiers[SourceEnv] = envTier

	res := &Resolution{
		values:    map[string]Value{},
		TierFiles: map[string]string{},
	}
	for _, k := range Schema {
		for _, src := range Precedence {
			raw, ok := tiers[src][k.Path]
			if !ok {
				continue
			}
			v, err := coerce(k, raw, src)
			if err != nil {
				return nil, err
			}
			res.values[k.Path] = Value{Key: k.Path, Value: v, Source: src}
			break
		}
		if _, ok := res.values[k.Path]; !ok {
			res.values[k.Path] = Value{Key: k.Path, Value: defaultOf(k), Source: SourceDefault}
		}
	}
	if in.LocalPath != "" {
		res.TierFiles["local"] = in.LocalPath
	}
	if in.ContractPath != "" {
		res.TierFiles["contract"] = in.ContractPath
	}
	return res, nil
}

func defaultOf(k Key) any {
	return k.Default
}

func parseFlags(flags map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for path, raw := range flags {
		k, ok := keyByPath(path)
		if !ok {
			// A --config typo is part of the invocation, so it is a usage error;
			// a bad value in a tier igdev read for itself is a config failure.
			return nil, contract.UsageFault(fmt.Sprintf("unknown config key %q passed with --config", path),
				contract.Remediation{Command: "igdev status --json", Why: "list the config keys igdev resolves"})
		}
		v, err := parseScalar(k, raw, SourceFlag)
		if err != nil {
			return nil, err
		}
		out[k.Path] = v
	}
	return out, nil
}

func parseEnv(schema []Key, env map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range schema {
		raw, ok := env[k.Env]
		if !ok || raw == "" {
			continue
		}
		v, err := parseScalar(k, raw, SourceEnv)
		if err != nil {
			return nil, err
		}
		out[k.Path] = v
	}
	return out, nil
}

// NotifierDisabledEnv is the frozen kill switch for the update notice. It is
// not a config key: it overrides every tier.
const NotifierDisabledEnv = "IGDEV_NO_UPDATE_NOTIFIER"

// NotifierDisabled reports whether the update notice is switched off by the
// environment.
func NotifierDisabled(env map[string]string) bool {
	switch strings.ToLower(env[NotifierDisabledEnv]) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func environMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		out[name] = value
	}
	return out
}

// EnvironMap exposes the environment-parsing helper to callers that need the
// frozen kill-switch check.
func EnvironMap(environ []string) map[string]string { return environMap(environ) }

// parseTOML flattens a TOML tier into dotted paths.
func parseTOML(label, path string, src []byte) (map[string]any, error) {
	if len(src) == 0 {
		return map[string]any{}, nil
	}
	var raw map[string]any
	if err := toml.Unmarshal(src, &raw); err != nil {
		return nil, configInvalid(fmt.Sprintf("%s %s is not valid TOML: %v", label, path, err),
			contract.Remediation{Command: "igdev status --json", Why: "re-check which config tier fails to parse"})
	}
	out := map[string]any{}
	flatten("", raw, out)
	return out, nil
}

func flatten(prefix string, in map[string]any, out map[string]any) {
	for k, v := range in {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if table, ok := v.(map[string]any); ok {
			flatten(path, table, out)
			continue
		}
		out[path] = v
	}
}

// coerce converts a value that a tier supplied, validating it against the key.
func coerce(k Key, raw any, src Source) (any, error) {
	switch v := raw.(type) {
	case string:
		return parseScalar(k, v, src)
	case bool:
		if k.Kind != KindBool {
			return nil, typeFault(k, src, fmt.Sprintf("expected %s, found boolean %t", typeName(k.Kind), v))
		}
		return v, nil
	case int64:
		return intFault(k, src, int(v))
	case int:
		return intFault(k, src, v)
	case float64:
		if k.Kind == KindInt && v == float64(int64(v)) {
			return int(v), nil
		}
		return nil, typeFault(k, src, fmt.Sprintf("expected %s", typeName(k.Kind)))
	default:
		return nil, typeFault(k, src, fmt.Sprintf("unsupported value of type %T", raw))
	}
}

func intFault(k Key, src Source, v int) (any, error) {
	if k.Kind != KindInt {
		return nil, typeFault(k, src, fmt.Sprintf("expected %s, found integer %d", typeName(k.Kind), v))
	}
	return v, nil
}

// parseScalar reads a string form of a value, as supplied by a flag or the
// environment.
func parseScalar(k Key, raw string, src Source) (any, error) {
	switch k.Kind {
	case KindBool:
		v, err := strconv.ParseBool(strings.ToLower(raw))
		if err != nil {
			switch strings.ToLower(raw) {
			case "0", "no", "off", "false":
				return false, nil
			case "1", "yes", "on", "true":
				return true, nil
			}
			return nil, typeFault(k, src, fmt.Sprintf("%q is not a boolean", raw))
		}
		return v, nil
	case KindInt:
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, typeFault(k, src, fmt.Sprintf("%q is not an integer", raw))
		}
		return v, nil
	default:
		if len(k.Oneof) > 0 && !contains(k.Oneof, raw) {
			return nil, typeFault(k, src, fmt.Sprintf("%q is not one of %s", raw, strings.Join(k.Oneof, ", ")))
		}
		return raw, nil
	}
}

// typeFault attributes a bad value to the tier that supplied it: the flag tier
// is the invocation (IGDEV_E_USAGE, exit 2), every other tier is machine or
// project state (IGDEV_E_CONFIG_INVALID, exit 1).
func typeFault(k Key, src Source, detail string) error {
	message := fmt.Sprintf("config key %q set by %s tier: %s", k.Path, src, detail)
	remediation := contract.Remediation{
		Command: "igdev status --json",
		Why:     "show the resolved value and the tier that set it",
	}
	if src == SourceFlag {
		return contract.UsageFault(message, remediation)
	}
	return configInvalid(message, remediation)
}

func configInvalid(message string, r ...contract.Remediation) error {
	return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure, message).WithRemediation(r...)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func typeName(k Kind) string {
	switch k {
	case KindBool:
		return "boolean"
	case KindInt:
		return "integer"
	default:
		return "string"
	}
}
