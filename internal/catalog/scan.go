package catalog

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Extensions are the file types a directory walk scans. An explicit file argument
// is scanned whatever its extension, which is what the bash specification's
// scan_paths does.
var Extensions = []string{".py", ".json", ".js", ".ts", ".tsx", ".java", ".kt", ".sh"}

// The three extraction patterns, ported verbatim from the bash specification's
// scan_file_capabilities: system.* references including nested namespaces, REST
// paths, and REST calls that name a method.
var (
	systemFunctionRE = regexp.MustCompile(`system\.[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+`)
	restPathRE       = regexp.MustCompile(`/data/[A-Za-z0-9._~%:{}@+,=-]+(/[A-Za-z0-9._~%:{}@+,=-]+)*/?`)
	restCallRE       = regexp.MustCompile(`(?i)(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)\s+(/data/[A-Za-z0-9._~%:{}@+,=-]+(/[A-Za-z0-9._~%:{}@+,=-]+)*/?)`)
)

// Occurrence is one capability reference found in one file, at one line. The
// line is what makes a scan result actionable: a preflight answer that names no
// location cannot be fixed.
type Occurrence struct {
	Capability string
	File       string
	Line       int
}

// ScanResult is what scanning a set of paths found.
type ScanResult struct {
	// Findings are every distinct capability occurrence, ordered by file, line,
	// and capability.
	Findings []Occurrence
	// Missing lists the requested paths that do not exist.
	Missing []string
	// Files is how many files were read.
	Files int
}

// Capabilities lists the distinct capabilities the findings reference, in
// first-appearance order.
func (r ScanResult) Capabilities() []string {
	seen := map[string]bool{}
	var out []string
	for _, finding := range r.Findings {
		if seen[finding.Capability] {
			continue
		}
		seen[finding.Capability] = true
		out = append(out, finding.Capability)
	}
	return out
}

// Scan reads every file under paths that can carry a capability reference. A
// directory is walked for the known extensions; a file is read as given. A path
// that does not exist is reported, not fatal: the bash specification warns and
// keeps going.
//
// A file that cannot be read is skipped, exactly as the bash specification's
// greps skip it; scanning is advisory about what it could read.
func Scan(paths []string) ScanResult {
	var out ScanResult
	for _, target := range paths {
		info, err := os.Stat(target)
		if err != nil {
			out.Missing = append(out.Missing, target)
			continue
		}
		if !info.IsDir() {
			out.scan(target)
			continue
		}
		_ = filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if !scannable(path) {
				return nil
			}
			out.scan(path)
			return nil
		})
	}
	sortFindings(out.Findings)
	return out
}

// scan adds one file's findings.
func (r *ScanResult) scan(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	r.Files++
	r.Findings = append(r.Findings, scanFile(path, raw)...)
}

// scannable reports whether a walked file has one of the known extensions.
func scannable(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	for _, candidate := range Extensions {
		if extension == candidate {
			return true
		}
	}
	return false
}

// scanFile extracts the capability references from one file's bytes.
//
// The bash specification greps whole files, so a reference never spans lines and
// a bare path is dropped when the same file also names a method for it: this
// keeps the method, which carries strictly more information.
func scanFile(path string, raw []byte) []Occurrence {
	var out []Occurrence
	methodPaths := map[string]bool{}
	type bare struct {
		path string
		line int
	}
	var bares []bare
	for i, line := range strings.Split(string(raw), "\n") {
		number := i + 1
		for _, match := range systemFunctionRE.FindAllString(line, -1) {
			out = append(out, Occurrence{Capability: match, File: path, Line: number})
		}
		for _, match := range restCallRE.FindAllStringSubmatch(line, -1) {
			method := strings.ToUpper(match[1])
			methodPaths[match[2]] = true
			out = append(out, Occurrence{Capability: method + " " + match[2], File: path, Line: number})
		}
		for _, match := range restPathRE.FindAllString(line, -1) {
			bares = append(bares, bare{path: match, line: number})
		}
	}
	for _, candidate := range bares {
		if methodPaths[candidate.path] {
			continue
		}
		out = append(out, Occurrence{Capability: candidate.path, File: path, Line: candidate.line})
	}
	return dedupe(out)
}

// dedupe drops repeated findings inside one file, keeping first appearance.
func dedupe(in []Occurrence) []Occurrence {
	seen := map[Occurrence]bool{}
	out := make([]Occurrence, 0, len(in))
	for _, finding := range in {
		if seen[finding] {
			continue
		}
		seen[finding] = true
		out = append(out, finding)
	}
	return out
}

// sortFindings orders findings by file, line, then capability.
func sortFindings(findings []Occurrence) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		switch {
		case a.File != b.File:
			return a.File < b.File
		case a.Line != b.Line:
			return a.Line < b.Line
		default:
			return a.Capability < b.Capability
		}
	})
}
