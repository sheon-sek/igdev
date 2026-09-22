package testrig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sheon-sek/igdev/internal/contract"
)

// EnvelopeKeyOrder is the frozen key order of the JSON envelope.
var EnvelopeKeyOrder = []string{"ok", "contract", "code", "message", "remediation", "data"}

// Envelope decodes machine stdout as the frozen envelope and asserts the key
// order, so a golden cannot silently drift.
func Envelope(t *testing.T, stdout string) contract.Envelope {
	t.Helper()
	if got := keyOrder(t, stdout); len(got) != 0 {
		assertStringSlice(t, EnvelopeKeyOrder, got)
	}
	var env contract.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not the JSON envelope: %v\n%s", err, stdout)
	}
	return env
}

// DataOf decodes the envelope's data member into a concrete struct.
func DataOf(t *testing.T, stdout string, target any) {
	t.Helper()
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &probe); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if err := json.Unmarshal(probe.Data, target); err != nil {
		t.Fatalf("decode data: %v", err)
	}
}

// WantExit asserts the observed exit level, naming the level in the failure.
func WantExit(t *testing.T, res Result, want contract.Exit) {
	t.Helper()
	if res.Exit != int(want) {
		t.Fatalf("exit level = %d, want %d (%s)\nstdout: %s\nstderr: %s",
			res.Exit, int(want), exitName(want), res.Stdout, res.Stderr)
	}
}

func exitName(e contract.Exit) string {
	switch e {
	case contract.ExitOK:
		return "success"
	case contract.ExitFailure:
		return "command failure"
	case contract.ExitUsage:
		return "usage error"
	case contract.ExitHumanAction:
		return "human action required"
	default:
		return "unknown level"
	}
}

// WantCode asserts the envelope's error code.
func WantCode(t *testing.T, env contract.Envelope, want contract.Code) {
	t.Helper()
	if env.Code != string(want) {
		t.Fatalf("code = %q, want %q (message %q)", env.Code, string(want), env.Message)
	}
}

// WantRemediation asserts that the envelope carries a machine-readable next step
// naming the given command.
func WantRemediation(t *testing.T, env contract.Envelope, command string) {
	t.Helper()
	for _, r := range env.Remediation {
		if r.Command == command {
			return
		}
	}
	t.Fatalf("remediation does not name %q: %+v", command, env.Remediation)
}

// StatusData decodes the data member of a status envelope. The shape mirrors
// cli.statusData; keeping it here lets tests read fields without importing the
// command implementation.
type StatusData struct {
	Initialized bool   `json:"initialized"`
	ProjectRoot string `json:"project_root"`
	WorkingDir  string `json:"working_dir"`
	Contract    struct {
		Path            string `json:"path"`
		Present         bool   `json:"present"`
		SchemaVersion   int    `json:"schema_version"`
		SchemaSupported bool   `json:"schema_supported"`
		Digest          string `json:"digest"`
	} `json:"contract"`
	Setup struct {
		Present    bool   `json:"present"`
		Path       string `json:"path"`
		StampState string `json:"stamp_state"`
		InstanceID string `json:"instance_id"`
		Namespace  string `json:"namespace"`
		Ports      *struct {
			HTTP  int `json:"http"`
			HTTPS int `json:"https"`
			Debug int `json:"debug"`
		} `json:"ports"`
	} `json:"setup"`
	Modules struct {
		Dir    string   `json:"dir"`
		Count  int      `json:"count"`
		Staged []string `json:"staged"`
	} `json:"modules"`
	Consent struct {
		Path  string `json:"path"`
		Terms []struct {
			ID         string `json:"id"`
			Accepted   bool   `json:"accepted"`
			AcceptedAt string `json:"accepted_at"`
			CLIVersion string `json:"cli_version"`
		} `json:"terms"`
	} `json:"consent"`
	Config struct {
		TierFiles map[string]string `json:"tier_files"`
		Resolved  []struct {
			Key    string `json:"key"`
			Value  any    `json:"value"`
			Source string `json:"source"`
		} `json:"resolved"`
	} `json:"config"`
}

// Status decodes a `igdev status --json` payload.
func Status(t *testing.T, stdout string) StatusData {
	t.Helper()
	var data StatusData
	DataOf(t, stdout, &data)
	return data
}

// ResolvedSource returns the winning tier reported for a config key.
func (s StatusData) ResolvedSource(key string) (source string, value any, ok bool) {
	for _, v := range s.Config.Resolved {
		if v.Key == key {
			return v.Source, v.Value, true
		}
	}
	return "", nil, false
}

// ResolvedValue returns the winning value reported for a config key.
func (s StatusData) ResolvedValue(key string) any {
	if _, value, ok := s.ResolvedSource(key); ok {
		return value
	}
	return nil
}

// CachePath is where the update notice caches its 24 h result in this
// environment: the XDG cache dir under the scratch HOME.
func (e *Env) CachePath(name string) string {
	return e.Path("home", ".cache", "igdev", name)
}

func keyOrder(t *testing.T, stdout string) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil
	}
	var keys []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, isString := tok.(string)
		if !isString {
			return keys
		}
		keys = append(keys, key)
		var value any
		if err := dec.Decode(&value); err != nil {
			return keys
		}
	}
}

func assertStringSlice(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("envelope key order = %v, want %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("envelope key order = %v, want %v", got, want)
		}
	}
}
