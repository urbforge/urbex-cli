// Package semver parses release tags (vX.Y.Z) and picks the latest and
// the next one. Releases of a project are Git tags of this form on its
// repo - see docs/decisions/0020-trunk-releases-gitops-environments.md in
// urbforge/urbex. Pre-release and build suffixes are not releases: Komodo
// versions are strictly major.minor.patch.
package semver

import (
	"fmt"
	"regexp"
	"strconv"
)

var pattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// Version is a release version.
type Version struct {
	Major, Minor, Patch int
}

// String is the version as deployed and as an image tag: "1.4.2".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Tag is the version as a Git tag: "v1.4.2".
func (v Version) Tag() string { return "v" + v.String() }

// NextPatch returns the version with its patch component incremented.
func (v Version) NextPatch() Version {
	return Version{v.Major, v.Minor, v.Patch + 1}
}

// After reports whether v is a later release than other.
func (v Version) After(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch > other.Patch
}

// Parse parses "vX.Y.Z" or "X.Y.Z". ok is false for anything else
// (pre-release suffixes, non-numeric components, ...).
func Parse(s string) (v Version, ok bool) {
	m := pattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return Version{major, minor, patch}, true
}

// Latest returns the highest version among tags, ignoring those that
// aren't releases. ok is false if there is none.
func Latest(tags []string) (latest Version, ok bool) {
	for _, t := range tags {
		v, isRelease := Parse(t)
		if !isRelease {
			continue
		}
		if !ok || v.After(latest) {
			latest, ok = v, true
		}
	}
	return latest, ok
}

// Next returns the release after the latest one in tags: its next patch
// version, or 0.1.0 for a project's first release.
func Next(tags []string) Version {
	latest, ok := Latest(tags)
	if !ok {
		return Version{0, 1, 0}
	}
	return latest.NextPatch()
}
