package textdiff

import "testing"

func TestUnifiedNewFile(t *testing.T) {
	got := Unified("igdev.toml", nil, []byte("a\nb\n"), 3)
	want := "--- /dev/null\n+++ b/igdev.toml\n@@ -0,0 +1,2 @@\n+a\n+b\n"
	if got != want {
		t.Errorf("Unified() =\n%q\nwant\n%q", got, want)
	}
}

func TestUnifiedIdenticalIsEmpty(t *testing.T) {
	if got := Unified("x", []byte("a\n"), []byte("a\n"), 3); got != "" {
		t.Errorf("identical inputs produced a diff: %q", got)
	}
}

// A change in the middle reports only the changed line plus the surrounding
// context, with line numbers that let a reader place it.
func TestUnifiedSingleChangeKeepsContext(t *testing.T) {
	old := []byte("one\ntwo\nthree\nfour\nfive\nsix\n")
	new := []byte("one\ntwo\nTHREE\nfour\nfive\nsix\n")
	want := "--- a/igdev.toml\n+++ b/igdev.toml\n@@ -1,6 +1,6 @@\n one\n two\n-three\n+THREE\n four\n five\n six\n"
	if got := Unified("igdev.toml", old, new, 3); got != want {
		t.Errorf("Unified() =\n%q\nwant\n%q", got, want)
	}
}

// Distant changes become separate hunks rather than one hunk swallowing the
// unchanged middle, which is what makes a contract diff readable.
func TestUnifiedSplitsDistantHunks(t *testing.T) {
	old := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n")
	new := []byte("one\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ntwelve\n")
	got := Unified("c.toml", old, new, 1)
	want := "--- a/c.toml\n+++ b/c.toml\n@@ -1,2 +1,2 @@\n-1\n+one\n 2\n@@ -11,2 +11,2 @@\n 11\n-12\n+twelve\n"
	if got != want {
		t.Errorf("Unified() =\n%q\nwant\n%q", got, want)
	}
}

// A pure deletion has no new-side lines in its hunk, and the header says so with
// the zero count convention.
func TestUnifiedDeletionOnly(t *testing.T) {
	old := []byte("a\nb\nc\n")
	new := []byte("a\nc\n")
	got := Unified("c.toml", old, new, 3)
	want := "--- a/c.toml\n+++ b/c.toml\n@@ -1,3 +1,2 @@\n a\n-b\n c\n"
	if got != want {
		t.Errorf("Unified() =\n%q\nwant\n%q", got, want)
	}
}

// A file too large for the LCS table still diffs correctly: every old line is
// removed and every new line added.
func TestUnifiedCoarseFallbackIsStillCorrect(t *testing.T) {
	big := make([]string, 1200)
	for i := range big {
		big[i] = "line"
	}
	old := []byte(join(big))
	got := Unified("big.toml", old, []byte("only\n"), 3)
	if got == "" {
		t.Fatal("a full replacement produced no diff")
	}
	if want := "@@ -1,1200 +1,1 @@\n"; !contains(got, want) {
		t.Errorf("coarse diff has no full-range hunk header:\n%s", got)
	}
	if want := "-line\n"; !contains(got, want) {
		t.Errorf("coarse diff lost the old lines:\n%s", firstN(got, 200))
	}
}

func join(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
