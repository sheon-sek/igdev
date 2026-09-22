package semver

import "testing"

// The update notice only fires when the advertised release really is newer, so
// the ordering rule is pinned here as well as through the binary.
func TestOrdering(t *testing.T) {
	ordered := []string{"0.0.9", "0.1.0", "v0.1.1", "0.2.0", "0.10.0", "1.0.0-rc1", "1.0.0"}
	for i := 1; i < len(ordered); i++ {
		low, err := Parse(ordered[i-1])
		if err != nil {
			t.Fatalf("parse %s: %v", ordered[i-1], err)
		}
		high, err := Parse(ordered[i])
		if err != nil {
			t.Fatalf("parse %s: %v", ordered[i], err)
		}
		if !low.Less(high) || !high.Greater(low) {
			t.Errorf("%s should sort before %s", ordered[i-1], ordered[i])
		}
		if high.Less(low) {
			t.Errorf("%s should not sort before %s", ordered[i], ordered[i-1])
		}
	}
}

// Build metadata is ignored, a leading "v" is cosmetic, and junk does not parse
// as a version.
func TestParsing(t *testing.T) {
	v, err := Parse("v1.2.3+build.9")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Major != 1 || v.Minor != 2 || v.Patch != 3 || v.Pre != "" {
		t.Errorf("parsed %+v", v)
	}
	for _, bad := range []string{"", "igdev", "1.2.3.4", "-1.0.0", "x.y.z"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", bad)
		}
	}
}

// Prerelease identifiers order field by field, with numeric fields below
// alphanumeric ones: the rule the notice must not get wrong.
func TestPrereleaseOrdering(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
	}
	for i := 1; i < len(ordered); i++ {
		if !MustParse(ordered[i-1]).Less(MustParse(ordered[i])) {
			t.Errorf("%s should sort before %s", ordered[i-1], ordered[i])
		}
	}
}
