package manifest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An application joins the network of another of its group, which joins
// none itself; it can't provide an interface (urbforge/urbex-cli#139).
func TestNetworkErrors(t *testing.T) {
	m := &Manifest{Services: []Service{
		{Name: "gluetun", Image: "qmcgaw/gluetun", Version: "3", Port: 8000, Healthcheck: "/", LXC: "dl", Capabilities: []string{"NET_ADMIN"}},
		{Name: "qbit", Image: "linuxserver/qbittorrent", Version: "5", Port: 8080, LXC: "dl", Network: "gluetun", Uses: []string{"gluetun"}},
		{Name: "self", Image: "x", Version: "1", LXC: "dl", Network: "self"},
		{Name: "ghost", Image: "x", Version: "1", LXC: "dl", Network: "nobody"},
		{Name: "away", Image: "x", Version: "1", LXC: "other", Network: "gluetun"},
		{Name: "solo", Image: "x", Version: "1", Network: "gluetun"},
		{Name: "chain", Image: "x", Version: "1", LXC: "dl", Network: "qbit"},
		{Name: "db", Image: "postgres", Version: "17", Provides: "postgres", LXC: "dl", Network: "gluetun", Volumes: []Volume{{Name: "data", Path: "/var/lib/postgresql"}}},
	}}
	got := m.networkErrors()
	want := []string{
		"services self: network is itself",
		"services ghost: network nobody, which urbex.yaml does not declare",
		"services away: joins the network of gluetun, which must be in the same group: give both the same lxc",
		"services solo: joins the network of gluetun, which must be in the same group: give both the same lxc",
		"services chain: joins the network of qbit, which joins gluetun's itself: join gluetun directly",
		"services db: provides postgres, which needs a network of its own: it can't join gluetun's",
	}
	if !slices.Equal(got, want) {
		t.Errorf("networkErrors:\n got %q\nwant %q", got, want)
	}
}

// network implies uses: the application starts once the other is ready.
func TestLoadTyped_NetworkImpliesUses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urbex.yaml")
	body := `apiVersion: urbex/v1
project: media
environments: [prod]
services:
  - name: gluetun
    image: qmcgaw/gluetun
    version: v3.40.0
    port: 8000
    healthcheck: /v1/publicip/ip
    lxc: download
    capabilities: [NET_ADMIN]
  - name: qbittorrent
    image: linuxserver/qbittorrent
    version: "5.1"
    port: 8080
    lxc: download
    network: gluetun
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, result, err := LoadTyped(path)
	if err != nil || m == nil {
		t.Fatalf("expected a valid manifest: %v %v", err, result)
	}
	if q := m.Services[1]; q.Network != "gluetun" || !slices.Equal(q.Uses, []string{"gluetun"}) {
		t.Errorf("qbittorrent: network %q, uses %v", q.Network, q.Uses)
	}
	if got := m.Unsupported(); len(got) != 0 {
		t.Errorf("Unsupported = %v", got)
	}
	bad := strings.Replace(body, "capabilities: [NET_ADMIN]", "capabilities: [SYS_ADMIN]", 1)
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, _, err := LoadTyped(path); err != nil || m != nil {
		t.Errorf("only NET_ADMIN is accepted: %v %v", m, err)
	}
}
