package gittag

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in     string
		want   Version
		wantOK bool
	}{
		{"v1.2.3", Version{1, 2, 3}, true},
		{"v0.1.0", Version{0, 1, 0}, true},
		{"1.2.3", Version{}, false}, // missing v prefix
		{"v1.2", Version{}, false},  // missing patch
		{"v1.2.3-rc1", Version{}, false},
		{"latest", Version{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.wantOK {
			t.Errorf("Parse(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestVersion_String(t *testing.T) {
	if got := (Version{1, 2, 3}).String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want v1.2.3", got)
	}
}

func TestVersion_NextPatch(t *testing.T) {
	got := (Version{1, 2, 3}).NextPatch()
	want := Version{1, 2, 4}
	if got != want {
		t.Errorf("NextPatch() = %+v, want %+v", got, want)
	}
}

func TestNextTag_NoExistingTags(t *testing.T) {
	if got := NextTag(nil); got != "v0.1.0" {
		t.Errorf("NextTag(nil) = %q, want v0.1.0", got)
	}
	if got := NextTag([]string{}); got != "v0.1.0" {
		t.Errorf("NextTag([]) = %q, want v0.1.0", got)
	}
}

func TestNextTag_PicksHighestAndBumpsPatch(t *testing.T) {
	got := NextTag([]string{"v1.0.0", "v1.2.3", "v1.2.0", "v0.9.9"})
	if got != "v1.2.4" {
		t.Errorf("NextTag(...) = %q, want v1.2.4", got)
	}
}

func TestNextTag_IgnoresUnparseableTags(t *testing.T) {
	got := NextTag([]string{"latest", "v1.0.0", "release-candidate"})
	if got != "v1.0.1" {
		t.Errorf("NextTag(...) = %q, want v1.0.1", got)
	}
}
