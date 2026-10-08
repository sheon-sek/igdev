// Package projectseed reads a project's tracked Gateway seed: the `[gateway] seed`
// directories, laid out like an Ignition resource collection, that igdev merges
// into the Instance image so the resources exist before the Gateway's first start
// (ADR 0009).
//
// A seed may carry any resource type except igdev's own (the Instance token and
// the security settings it seeds), and two seed directories may not write the
// same file. A JSON file that carries a secret field with a value is loaded, with
// a warning: the seed is tracked, but the Gateway it builds is a disposable
// development Gateway (ADR 0009, amendment 1). Every refusal and warning names the
// offending path. The result is deterministic: files in path order and a digest over their
// names and bytes, which the Setup Stamp records so a seed change makes the
// checkout stale.
package projectseed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Limits bound what a seed copies into the image: a file or a total beyond them
// is refused, never truncated. They are far above any real configuration — a
// large tag export included.
const (
	MaxFileBytes  = 64 << 20
	MaxTotalBytes = 512 << 20
)

// Reserved are the resource types igdev seeds itself (ADR 0007). A project seed
// that names one would replace the security settings or the Instance token, so it
// is a collision, not an unlisted type.
var Reserved = []string{
	"ignition/api-token",
	"ignition/security-levels",
	"ignition/security-properties",
}

// secretKeys are the JSON keys whose value is a credential. Matching ignores case
// and punctuation, so client_secret and clientSecret are both caught.
var secretKeys = []string{"password", "passwd", "secret", "privatekey", "apikey", "token", "tokenhash", "credential", "credentials"}

// File is one seed file: its path inside the resource collection and its bytes.
type File struct {
	// Path is slash-separated, <module>/<type>/<name>/<file>.
	Path string
	Data []byte
}

// Seed is a loaded project seed.
type Seed struct {
	Files []File
	// Digest is a sha256 over every file's path and bytes, or "" when there are
	// no directories.
	Digest string
	// Warnings name the files that carry a secret field with a value.
	Warnings []string
}

// Load reads the seed directories, relative to root, and applies every rule.
func Load(root string, dirs []string) (Seed, error) {
	if len(dirs) == 0 {
		return Seed{}, nil
	}
	byPath := map[string]string{}
	var files []File
	var warnings []string
	total := 0
	for _, dir := range dirs {
		base := filepath.Join(root, filepath.FromSlash(dir))
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			return Seed{}, invalid(fmt.Sprintf("gateway.seed entry %q is not a directory in the repository", dir))
		}
		err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, p)
			rel = filepath.ToSlash(rel)
			shown := path.Join(dir, rel)
			if d.Type()&fs.ModeSymlink != 0 {
				return invalid(fmt.Sprintf("seed path %s is a symlink; a seed holds regular files only", shown))
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return invalid(fmt.Sprintf("seed path %s is not a regular file", shown))
			}
			if err := checkType(rel, shown); err != nil {
				return err
			}
			if first, ok := byPath[rel]; ok {
				return invalid(fmt.Sprintf("seed path %s collides with %s: two seed directories write %s", shown, first, rel))
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return invalid(fmt.Sprintf("seed path %s cannot be read: %v", shown, err))
			}
			if len(data) > MaxFileBytes {
				return invalid(fmt.Sprintf("seed path %s is %d bytes, over the %d-byte limit", shown, len(data), MaxFileBytes))
			}
			total += len(data)
			if total > MaxTotalBytes {
				return invalid(fmt.Sprintf("the seed directories hold more than %d bytes", MaxTotalBytes))
			}
			if strings.HasSuffix(rel, ".json") {
				warning, err := checkSecrets(data, shown)
				if err != nil {
					return err
				}
				if warning != "" {
					warnings = append(warnings, warning)
				}
			}
			byPath[rel] = shown
			files = append(files, File{Path: rel, Data: data})
			return nil
		})
		if err != nil {
			if fault, ok := err.(*contract.Fault); ok {
				return Seed{}, fault
			}
			return Seed{}, invalid(fmt.Sprintf("gateway.seed entry %q cannot be read: %v", dir, err))
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Data))
		h.Write(f.Data)
	}
	return Seed{Files: files, Digest: hex.EncodeToString(h.Sum(nil)), Warnings: warnings}, nil
}

// Digest is the seed digest for the Setup Stamp. A seed that cannot be loaded
// digests to a value no setup records, so the checkout reads stale and `setup`
// reports the real problem.
func Digest(root string, dirs []string) string {
	seed, err := Load(root, dirs)
	if err != nil {
		return "invalid"
	}
	return seed.Digest
}

// checkType places a file at <module>/<type>/<name>/<file> and refuses igdev's
// own resource types, which a project seed would otherwise override.
func checkType(rel, shown string) error {
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 {
		kind := parts[0] + "/" + parts[1]
		for _, reserved := range Reserved {
			if kind == reserved {
				return invalid(fmt.Sprintf("seed path %s collides with igdev's own %s resource, which a project seed cannot override", shown, kind))
			}
		}
	}
	if len(parts) < 4 {
		return invalid(fmt.Sprintf("seed path %s is not inside <module>/<type>/<name>/", shown))
	}
	return nil
}

// checkSecrets reports a JSON file with a secret-named key that holds a value as
// a warning, not a refusal. An empty string, null, or an empty object is no
// secret. A file that is not JSON is still refused: the Gateway could not read it.
func checkSecrets(data []byte, shown string) (string, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", invalid(fmt.Sprintf("seed path %s is not valid JSON: %v", shown, err))
	}
	if key, found := findSecret(doc, ""); found {
		return fmt.Sprintf("seed path %s carries a secret at %s, and the seed is tracked in git", shown, key), nil
	}
	return "", nil
}

func findSecret(v any, at string) (string, bool) {
	switch node := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			where := at + "." + k
			if secretKey(k) && hasValue(node[k]) {
				return where, true
			}
			if key, found := findSecret(node[k], where); found {
				return key, true
			}
		}
	case []any:
		for i, item := range node {
			if key, found := findSecret(item, fmt.Sprintf("%s[%d]", at, i)); found {
				return key, true
			}
		}
	}
	return "", false
}

func secretKey(k string) bool {
	norm := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(k))
	for _, s := range secretKeys {
		if strings.HasSuffix(norm, s) {
			return true
		}
	}
	return false
}

func hasValue(v any) bool {
	switch val := v.(type) {
	case nil:
		return false
	case string:
		return val != ""
	case map[string]any:
		return len(val) > 0
	case []any:
		return len(val) > 0
	default:
		return true
	}
}

func invalid(message string) *contract.Fault {
	return contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure, message).
		WithRemediation(contract.Remediation{
			Command: "igdev setup",
			Why:     "rerun once the seed directory is fixed; see ADR 0009 for what a seed may hold",
		})
}
