package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
)

func TestRestoreHostname(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "urbex.yaml")
	data, err := os.ReadFile("../../e2e/groups/urbex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &platform.Config{}

	cases := []struct{ target, machine, want, err string }{
		{"platform", "gitea", "urbex-gitea", ""},
		{"platform", "urbex-keycloak", "urbex-keycloak", ""},
		{"platform", "tunnel", "", "one of gitea, komodo, technitium, keycloak, observability"},
		{"prod", "pair", "box-pair-prod", ""},
		{"prod", "who", "box-pair-prod", ""},
		{"prod", "solo", "box-solo-prod", ""},
		{"prod", "box-solo-prod", "box-solo-prod", ""},
		{"prod", "nope", "", "one of pair, solo"},
		{"staging", "solo", "", `no environment "staging": one of prod, or platform`},
	}
	for _, c := range cases {
		got, err := restoreHostname(c.target, c.machine, manifest, cfg)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s/%s: err = %v, want %q", c.target, c.machine, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s/%s = %q, %v; want %q", c.target, c.machine, got, err, c.want)
		}
	}
}
