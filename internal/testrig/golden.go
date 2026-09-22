package testrig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// GoldenDir is where per-command golden files live, relative to the test
// package's working directory. itest keeps its goldens in
// itest/testdata/golden.
var GoldenDir = filepath.Join("testdata", "golden")

// UpdateGoldens reports whether goldens should be rewritten instead of
// compared. Run a suite once with IGDEV_UPDATE_GOLDENS=1 after an intentional
// contract change, then read the diff.
func UpdateGoldens() bool {
	switch strings.ToLower(os.Getenv("IGDEV_UPDATE_GOLDENS")) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// RegisterReplacement makes a volatile string (an absolute scratch path, a
// loopback URL with a random port) collapse to a stable token in goldens.
func (e *Env) RegisterReplacement(value, token string) {
	if value == "" {
		return
	}
	e.replacements = append(e.replacements, replacement{value: value, token: token})
}

type replacement struct {
	value string
	token string
}

// Normalize rewrites every volatile part of observed output so a golden can be a
// literal. Longest values win, which keeps HOME from being eaten by ROOT.
func (e *Env) Normalize(s string) string {
	list := append([]replacement{
		{value: e.Binary, token: "<BIN>"},
		{value: e.Home, token: "<HOME>"},
		{value: e.Temp, token: "<TMP>"},
		{value: e.Shim, token: "<SHIM>"},
		{value: e.Root, token: "<ROOT>"},
	}, e.replacements...)
	sort.SliceStable(list, func(i, j int) bool { return len(list[i].value) > len(list[j].value) })
	seen := map[string]bool{}
	for _, r := range list {
		if r.value == "" || seen[r.value] {
			continue
		}
		seen[r.value] = true
		s = strings.ReplaceAll(s, r.value, r.token)
	}
	return s
}

// Golden compares normalized got against testdata/golden/<name>, and rewrites the
// file when IGDEV_UPDATE_GOLDENS=1.
func (e *Env) Golden(t *testing.T, name, got string) {
	t.Helper()
	e.compareGolden(t, filepath.Join(GoldenDir, name), e.Normalize(got))
}

func (e *Env) compareGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if UpdateGoldens() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	if os.IsNotExist(err) {
		t.Fatalf("golden %s does not exist; create it with IGDEV_UPDATE_GOLDENS=1 go test -run %s\n--- observed ---\n%s",
			path, t.Name(), got)
	}
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %s mismatch (-want +got):\n%s", path, lineDiff(string(want), got))
	}
}

// lineDiff renders a compact, line-oriented diff for mismatch messages.
func lineDiff(want, got string) string {
	wl := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gl := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var b strings.Builder
	max := len(wl)
	if len(gl) > max {
		max = len(gl)
	}
	for i := range max {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		fmt.Fprintf(&b, "line %d:\n  want: %q\n  got:  %q\n", i+1, w, g)
	}
	if b.Len() == 0 {
		return "identical lines (trailing newline differs)"
	}
	return b.String()
}
