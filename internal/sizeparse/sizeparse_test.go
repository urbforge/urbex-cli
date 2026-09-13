package sizeparse

import "testing"

func TestMB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"512Mi", 512},
		{"1Gi", 1024},
		{"2Gi", 2048},
	}
	for _, c := range cases {
		got, err := MB(c.in)
		if err != nil {
			t.Errorf("MB(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("MB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMB_InvalidSuffix(t *testing.T) {
	if _, err := MB("5"); err == nil {
		t.Error("expected an error for a missing suffix")
	}
	if _, err := MB("5Ki"); err == nil {
		t.Error("expected an error for an unsupported suffix")
	}
}

func TestGB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"5Gi", 5},
		{"512Mi", 1}, // rounds up
		{"2048Mi", 2},
		{"100Mi", 1}, // floor of 1
	}
	for _, c := range cases {
		got, err := GB(c.in)
		if err != nil {
			t.Errorf("GB(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("GB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
