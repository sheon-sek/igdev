// Package textdiff renders the unified diff igdev prints whenever it writes a
// tracked file. A Contract write is a repository change, so the diff is the
// review surface: humans read it on stderr, agents read it in the envelope.
package textdiff

import (
	"fmt"
	"strings"
)

// maxCells bounds the LCS table. Contracts are a few dozen lines; a file large
// enough to blow the budget still gets a correct diff, just a coarse one.
const maxCells = 1 << 20

// Unified renders old against new as a unified diff with context lines of
// context, under the file name name. New files diff against /dev/null. It
// returns "" when the two are identical.
func Unified(name string, old, new []byte, context int) string {
	if string(old) == string(new) {
		return ""
	}
	before, after := lines(old), lines(new)

	var out strings.Builder
	if old == nil {
		fmt.Fprintf(&out, "--- /dev/null\n+++ b/%s\n", name)
	} else {
		fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", name, name)
	}

	script := edits(before, after)
	if len(script) == 0 {
		return ""
	}

	changed := make([]int, 0, len(script))
	for i, e := range script {
		if e.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return ""
	}

	for start := 0; start < len(changed); {
		first := changed[start]
		last := first
		next := start + 1
		for next < len(changed) && changed[next]-last <= 2*context {
			last = changed[next]
			next++
		}
		start = next

		from := first - context
		if from < 0 {
			from = 0
		}
		to := last + 1 + context
		if to > len(script) {
			to = len(script)
		}
		oldStart, newStart := 1, 1
		for _, e := range script[:from] {
			oldStart, newStart = advance(oldStart, newStart, e)
		}
		oldCount, newCount := 0, 0
		for _, e := range script[from:to] {
			if e.kind != '+' {
				oldCount++
			}
			if e.kind != '-' {
				newCount++
			}
		}
		if oldCount == 0 {
			oldStart = 0
		}
		if newCount == 0 {
			newStart = 0
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, e := range script[from:to] {
			out.WriteByte(e.kind)
			out.WriteString(e.text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func advance(oldStart, newStart int, e edit) (int, int) {
	if e.kind != '+' {
		oldStart++
	}
	if e.kind != '-' {
		newStart++
	}
	return oldStart, newStart
}

// edit is one line of the edit script: an unchanged line, a deletion from the
// old side, or an insertion on the new side.
type edit struct {
	kind byte
	text string
}

// edits computes a line-level edit script with a longest-common-subsequence
// table. Output is deterministic: equal lines are matched first, deletions are
// emitted before insertions.
func edits(a, b []string) []edit {
	if len(a)*len(b) > maxCells {
		return coarse(a, b)
	}
	// table[i][j] is the LCS length of a[i:] and b[j:].
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	out := make([]edit, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, edit{' ', a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			out = append(out, edit{'-', a[i]})
			i++
		default:
			out = append(out, edit{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, edit{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, edit{'+', b[j]})
	}
	return out
}

// coarse is the fallback for a file too large for the LCS table: everything old
// is deleted and everything new is inserted. The diff is still correct, just
// unrefined.
func coarse(a, b []string) []edit {
	out := make([]edit, 0, len(a)+len(b))
	for _, line := range a {
		out = append(out, edit{'-', line})
	}
	for _, line := range b {
		out = append(out, edit{'+', line})
	}
	return out
}

// lines splits text into lines, dropping the final empty element a trailing
// newline produces so it never shows up as a phantom edit.
func lines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	text := strings.TrimSuffix(string(raw), "\n")
	return strings.Split(text, "\n")
}
