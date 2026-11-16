package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func withVars(t *testing.T, v, c, d string) {
	t.Helper()
	ov, oc, od := Version, Commit, Date
	Version, Commit, Date = v, c, d
	t.Cleanup(func() { Version, Commit, Date = ov, oc, od })
}

func TestGet_ReleaseBuild(t *testing.T) {
	withVars(t, "v0.1.0", "1a2b3c4d5e6f7a8b9c0d", "2026-11-16T10:00:00Z")
	// What ldflags set wins over what Go embedded.
	bi := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "ffffffff"}}}
	i := get(bi)
	if i.Version != "0.1.0" || i.Commit != "1a2b3c4d5e6f7a8b9c0d" || i.Date != "2026-11-16T10:00:00Z" {
		t.Fatalf("got %+v", i)
	}
	s := i.String()
	if !strings.HasPrefix(s, "urbex 0.1.0 (commit 1a2b3c4, built 2026-11-16T10:00:00Z, ") {
		t.Errorf("String() = %q", s)
	}
}

func TestGet_LocalBuild(t *testing.T) {
	withVars(t, "dev", "", "")
	bi := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef0123456789"},
		{Key: "vcs.time", Value: "2026-11-15T09:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}
	i := get(bi)
	if i.Version != "dev" || i.Commit != "abcdef0123456789" || !i.Modified || i.Date != "" || i.CommitDate != "2026-11-15T09:00:00Z" {
		t.Fatalf("got %+v", i)
	}
	if s := i.String(); !strings.HasPrefix(s, "urbex dev (commit abcdef0-dirty, committed 2026-11-15T09:00:00Z, ") {
		t.Errorf("String() = %q", s)
	}
}

func TestGet_NoBuildInfo(t *testing.T) {
	withVars(t, "dev", "", "")
	i := get(nil)
	if i.Commit != "" || i.Go == "" || i.Platform == "" {
		t.Fatalf("got %+v", i)
	}
	if s := i.String(); !strings.HasPrefix(s, "urbex dev (") {
		t.Errorf("String() = %q", s)
	}
}
