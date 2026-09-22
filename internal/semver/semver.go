// Package semver parses and compares the release version strings igdev
// compares against GitHub Release tags. It is deliberately small: only what the
// update notice needs, with no dependencies.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Build metadata is ignored.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string
}

// Parse accepts an optional leading "v" and the usual major.minor.patch form,
// with optional -prerelease and +build suffixes.
func Parse(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	v := Version{Raw: raw}
	body := strings.TrimPrefix(raw, "v")
	if i := strings.IndexByte(body, '+'); i >= 0 {
		body = body[:i]
	}
	if i := strings.IndexByte(body, '-'); i >= 0 {
		v.Pre = body[i+1:]
		body = body[:i]
	}
	parts := strings.Split(body, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, fmt.Errorf("%q is not a version", raw)
	}
	dest := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%q is not a version", raw)
		}
		*dest[i] = n
	}
	return v, nil
}

// MustParse panics on invalid input; used for compile-time constants.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Less reports whether v sorts before other.
func (v Version) Less(other Version) bool {
	if c := compareNumeric(v, other); c != 0 {
		return c < 0
	}
	switch {
	case v.Pre == other.Pre:
		return false
	case v.Pre == "":
		return false // release beats prerelease
	case other.Pre == "":
		return true
	default:
		return comparePre(v.Pre, other.Pre) < 0
	}
}

// Greater reports whether v sorts after other.
func (v Version) Greater(other Version) bool { return other.Less(v) }

func compareNumeric(a, b Version) int {
	for _, pair := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePre orders two prerelease strings field by field, numeric fields
// sorting below alphanumeric ones.
func comparePre(a, b string) int {
	af := strings.Split(a, ".")
	bf := strings.Split(b, ".")
	for i := 0; i < len(af) && i < len(bf); i++ {
		if c := comparePreField(af[i], bf[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(af) == len(bf):
		return 0
	case len(af) < len(bf):
		return -1
	default:
		return 1
	}
}

func comparePreField(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
