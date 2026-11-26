// Package tag implements repository-wide `vX.Y.Z[-pre]` tag parsing and
// resolution rules (DESIGN §6, D83).
package tag

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Build metadata is ignored for ordering.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string
}

// String returns the version without build metadata.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders versions; a pre-release sorts before its release.
func (v Version) Compare(o Version) int {
	if c := cmpInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case v.Pre == "" && o.Pre == "":
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	default:
		return strings.Compare(v.Pre, o.Pre)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ParseVersion parses `vX.Y.Z[-pre]`.
func ParseVersion(s string) (Version, error) {
	if !strings.HasPrefix(s, "v") {
		return Version{}, fmt.Errorf("version %q must start with 'v'", s)
	}
	body := s[1:]
	pre := ""
	if i := strings.IndexByte(body, '-'); i >= 0 {
		pre = body[i+1:]
		body = body[:i]
	}
	parts := strings.Split(body, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q must be vMAJOR.MINOR.PATCH", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q has a non-numeric component %q", s, p)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre, Raw: s}, nil
}

// versionOf reads a tag as a repository-wide `vX.Y.Z` (D83). A tag carrying a
// package prefix (`pkg/vX.Y.Z`, `pkg@vX.Y.Z`) is ignored: one repository is one
// package, so a prefixed tag would be ambiguous.
func versionOf(raw string) (Version, bool) {
	v, err := ParseVersion(raw)
	if err != nil {
		return Version{}, false
	}
	return v, true
}

// Find returns the tag name and version that `ref` selects.
func Find(ref string, tags []string) (string, Version, error) {
	type candidate struct {
		raw string
		v   Version
	}
	var cands []candidate
	for _, t := range tags {
		if v, ok := versionOf(t); ok {
			cands = append(cands, candidate{t, v})
		}
	}
	if len(cands) == 0 {
		return "", Version{}, fmt.Errorf("no version tags (want vX.Y.Z)")
	}

	switch ref {
	case "", "latest":
		sort.Slice(cands, func(i, j int) bool { return cands[i].v.Compare(cands[j].v) > 0 })
		return cands[0].raw, cands[0].v, nil
	case "main", "master":
		return "", Version{}, fmt.Errorf("branch %q is not a version; pin a tag or a commit", ref)
	}

	normalised := ref
	if !strings.HasPrefix(normalised, "v") {
		normalised = "v" + normalised
	}
	want, err := ParseVersion(normalised)
	if err != nil {
		return "", Version{}, err
	}
	for _, c := range cands {
		if c.v.Compare(want) == 0 {
			return c.raw, c.v, nil
		}
	}
	return "", Version{}, fmt.Errorf("no version %s", want)
}

// Latest returns the highest repository-wide version.
func Latest(tags []string) (Version, error) {
	var versions []Version
	for _, t := range tags {
		if v, ok := versionOf(t); ok {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return Version{}, fmt.Errorf("no version tags (want vX.Y.Z)")
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Compare(versions[j]) > 0 })
	return versions[0], nil
}

// Match resolves a user reference ("latest", "v0.0.1", "0.0.1").
func Match(ref string, tags []string) (Version, error) {
	switch ref {
	case "", "latest":
		return Latest(tags)
	case "main", "master":
		return Version{}, fmt.Errorf("branch %q is not a version; pin a tag or a commit", ref)
	}
	normalised := ref
	if !strings.HasPrefix(normalised, "v") {
		normalised = "v" + normalised
	}
	want, err := ParseVersion(normalised)
	if err != nil {
		return Version{}, err
	}
	for _, t := range tags {
		if v, ok := versionOf(t); ok && v.Compare(want) == 0 {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("no version %s", want)
}

// MajorOf returns the major version number for a valid semantic version tag
// (`vX.Y.Z[-pre]`), or 0 otherwise.
func MajorOf(tagName string) int {
	v, err := ParseVersion(tagName)
	if err != nil {
		return 0
	}
	return v.Major
}

// LatestInMajor returns the highest semantic version tag in the given major
// version series (e.g. major=2 selects the highest v2.x.y tag).
func LatestInMajor(tags []string, major int) (string, bool) {
	var best Version
	found := false
	for _, t := range tags {
		v, ok := versionOf(t)
		if !ok || v.Major != major {
			continue
		}
		if !found || v.Compare(best) > 0 {
			best = v
			found = true
		}
	}
	if !found {
		return "", false
	}
	return best.Raw, true
}
