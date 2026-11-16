// Package version is the urbex binary's version, so that a bug report can
// say which build it came from.
//
// A release build sets Version, Commit and Date with -ldflags -X, e.g.
//
//	go build -ldflags "-X github.com/urbforge/urbex-cli/internal/version.Version=0.1.0 \
//	  -X github.com/urbforge/urbex-cli/internal/version.Commit=$(git rev-parse HEAD) \
//	  -X github.com/urbforge/urbex-cli/internal/version.Date=$(date -u +%FT%TZ)" ./cmd/urbex
//
// A plain `go build` leaves Version "dev" and takes the commit, its date
// and whether the tree had changes from what Go embeds in the binary (debug.ReadBuildInfo).
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set at build time with -ldflags -X.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is what `urbex version` prints.
type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit,omitempty"`
	Date       string `json:"date,omitempty"`        // build date, set by a release build
	CommitDate string `json:"commit_date,omitempty"` // a build from source: the commit's date
	Modified   bool   `json:"modified,omitempty"`    // built from a tree with uncommitted changes
	Go         string `json:"go"`
	Platform   string `json:"platform"`
}

// Get returns this binary's Info.
func Get() Info {
	bi, _ := debug.ReadBuildInfo()
	return get(bi)
}

func get(bi *debug.BuildInfo) Info {
	i := Info{
		Version:  strings.TrimPrefix(Version, "v"),
		Commit:   Commit,
		Date:     Date,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if i.Commit != "" || bi == nil {
		return i
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			i.Commit = s.Value
		case "vcs.time":
			i.CommitDate = s.Value
		case "vcs.modified":
			i.Modified = s.Value == "true"
		}
	}
	return i
}

// ShortCommit is the commit's first 7 characters.
func (i Info) ShortCommit() string {
	if len(i.Commit) > 7 {
		return i.Commit[:7]
	}
	return i.Commit
}

// String is one line: "urbex 0.1.0 (commit 1a2b3c4, built 2026-11-16T10:00:00Z, linux/amd64)",
// or from source "urbex dev (commit 1a2b3c4-dirty, committed 2026-11-15T09:00:00Z, linux/amd64)".
func (i Info) String() string {
	var parts []string
	if c := i.ShortCommit(); c != "" {
		if i.Modified {
			c += "-dirty"
		}
		parts = append(parts, "commit "+c)
	}
	if i.Date != "" {
		parts = append(parts, "built "+i.Date)
	} else if i.CommitDate != "" {
		parts = append(parts, "committed "+i.CommitDate)
	}
	parts = append(parts, i.Platform)
	return fmt.Sprintf("urbex %s (%s)", i.Version, strings.Join(parts, ", "))
}
