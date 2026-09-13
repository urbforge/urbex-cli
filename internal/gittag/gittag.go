// Package gittag parses semver-style Git tags (vX.Y.Z) and computes the
// next release version for `urbex promote`. See
// docs/decisions/0010-promotion-flow.md in urbforge/urbex: promotion is
// always Git-based, via a tag.
package gittag

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var pattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// Version is a parsed vX.Y.Z tag.
type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// NextPatch returns the version with its patch component incremented.
func (v Version) NextPatch() Version {
	return Version{v.Major, v.Minor, v.Patch + 1}
}

// after reports whether v is a later release than other.
func (v Version) after(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch > other.Patch
}

// Parse parses a "vX.Y.Z" tag. ok is false for anything else (pre-release
// suffixes, missing "v" prefix, non-numeric components, ...).
func Parse(tag string) (v Version, ok bool) {
	m := pattern.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return Version{major, minor, patch}, true
}

// NextTag returns the next patch version after the highest vX.Y.Z tag
// found in existing (tags that don't parse as vX.Y.Z are ignored), or
// "v0.1.0" if none do.
func NextTag(existing []string) string {
	var highest Version
	found := false
	for _, t := range existing {
		v, ok := Parse(t)
		if !ok {
			continue
		}
		if !found || v.after(highest) {
			highest = v
			found = true
		}
	}
	if !found {
		return "v0.1.0"
	}
	return highest.NextPatch().String()
}

// ListTags lists the Git tags of the repo at dir matching pattern (a Git
// glob, e.g. "v*"), via `git tag --list`. Read-only, so it shells out
// directly rather than through a runner.CommandRunner.
func ListTags(dir, globPattern string) ([]string, error) {
	cmd := exec.Command("git", "tag", "--list", globPattern)
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag --list: %w", err)
	}

	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			tags = append(tags, line)
		}
	}
	return tags, nil
}
