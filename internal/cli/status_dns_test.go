package cli

import "testing"

func TestDNSState(t *testing.T) {
	cases := []struct {
		got  []string
		want string
	}{
		{nil, "api.urbex.lab (missing)"},
		{[]string{"10.0.0.5"}, "api.urbex.lab"},
		{[]string{"10.0.0.9"}, "api.urbex.lab (points to 10.0.0.9, expected 10.0.0.5)"},
		{[]string{"10.0.0.5", "10.0.0.9"}, "api.urbex.lab (points to 10.0.0.5,10.0.0.9, expected 10.0.0.5)"},
	}
	for _, tc := range cases {
		if got := dnsState("api.urbex.lab", "10.0.0.5", tc.got); got != tc.want {
			t.Errorf("dnsState(%v) = %q, want %q", tc.got, got, tc.want)
		}
	}
}

// Without a Technitium token the name is still shown, unchecked.
func TestDNSChecker_NilShowsTheNameUnchecked(t *testing.T) {
	var d *dnsChecker
	if got := d.Check(nil, "api.urbex.lab", "10.0.0.5"); got != "dns=api.urbex.lab (not checked: no technitium token)" {
		t.Errorf("nil checker = %q", got)
	}
}
