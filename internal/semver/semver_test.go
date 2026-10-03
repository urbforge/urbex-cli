package semver

import "testing"

func TestParse(t *testing.T) {
	for in, want := range map[string]string{"v1.4.2": "1.4.2", "0.1.0": "0.1.0", "v10.20.30": "10.20.30"} {
		v, ok := Parse(in)
		if !ok || v.String() != want {
			t.Errorf("Parse(%q) = %v, %v; want %s", in, v, ok, want)
		}
	}
	for _, in := range []string{"v1.2", "v1.2.3-rc.1", "release-1.2.3", "v1.2.x", ""} {
		if _, ok := Parse(in); ok {
			t.Errorf("Parse(%q) should not be a release", in)
		}
	}
	if v, _ := Parse("1.4.2"); v.Tag() != "v1.4.2" {
		t.Errorf("Tag() = %q", v.Tag())
	}
}

func TestLatestAndNext(t *testing.T) {
	tags := []string{"v0.9.9", "v1.2.10", "v1.2.9", "nightly", "v1.3.0-rc.1"}
	latest, ok := Latest(tags)
	if !ok || latest.String() != "1.2.10" {
		t.Errorf("Latest = %v, %v; want 1.2.10 (numeric, not lexical, and no pre-releases)", latest, ok)
	}
	if got := Next(tags).Tag(); got != "v1.2.11" {
		t.Errorf("Next = %s, want v1.2.11", got)
	}
	if _, ok := Latest([]string{"nightly"}); ok {
		t.Error("Latest should report no release among non-release tags")
	}
	if got := Next(nil).Tag(); got != "v0.1.0" {
		t.Errorf("first release = %s, want v0.1.0", got)
	}
}
