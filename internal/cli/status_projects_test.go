package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
)

// A container named web is a service like any other; only a web
// frontend's compose file makes a Worker of it (urbforge/urbex-cli#66).
func TestPrintProjects_ContainerNamedWeb(t *testing.T) {
	repo := envrepo.Repo{Path: t.TempDir()}
	container := manifest.Service{Name: "web", Runtime: "docker", Image: "nginx:1.27", Port: 8081, Public: true}
	if err := repo.WriteService("prod", "shop", container, "nginx", false, nil); err != nil {
		t.Fatal(err)
	}
	frontend := manifest.Service{Name: manifest.WebService, Runtime: manifest.WebRuntime}
	if err := repo.WriteService("prod", "site", frontend, "reg/urbex/site-web", false, nil); err != nil {
		t.Fatal(err)
	}
	if !repo.WebFrontend("prod", "site", "web") || repo.WebFrontend("prod", "shop", "web") {
		t.Fatal("WebFrontend should tell the frontend from the container")
	}
	ledger, _ := allocation.Load(repo.Path+"/state/allocations.json", 0, 0)
	ledger.Entries = map[string]allocation.Entry{provision.Hostname("shop", "web", "prod"): {VMID: 9121, IP: "192.0.2.45"}}
	if err := ledger.Save(repo.Path + "/state/allocations.json"); err != nil {
		t.Fatal(err)
	}

	defer func(f func(context.Context, string) (string, error)) { quickTunnelURL = f }(quickTunnelURL)
	quickTunnelURL = func(_ context.Context, ip string) (string, error) {
		if ip != "192.0.2.45" {
			t.Errorf("asked %s for its Quick Tunnel", ip)
		}
		return "https://random-words.trycloudflare.com", nil
	}

	for _, tc := range []struct {
		name string
		cf   platform.CloudflareConfig
		shop string
		site string
	}{
		{"quick tunnel", platform.CloudflareConfig{QuickTunnel: true}, "https://random-words.trycloudflare.com", ""},
		{"cloudflare", platform.CloudflareConfig{AccountID: "a", ZoneID: "z", Name: "urbex"}, "", "https://site-urbex.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &platform.Config{Domain: "example.com", Cloudflare: tc.cf}
			var out bytes.Buffer
			if err := printProjects(context.Background(), &out, cfg, &credentials.Credentials{}, repo.Path); err != nil {
				t.Fatal(err)
			}
			var shop, site string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "web ") {
					if shop == "" {
						shop = line
					} else {
						site = line
					}
				}
			}
			// Projects are listed in order: shop, then site.
			for _, want := range []string{"http://192.0.2.45:8081", "dns=" + cfg.ServiceInternalName("shop", "prod", "web"), tc.shop} {
				if !strings.Contains(shop, want) {
					t.Errorf("shop's web lacks %q: %q", want, shop)
				}
			}
			if !strings.Contains(site, " - ") || (tc.site != "" && !strings.Contains(site, tc.site)) {
				t.Errorf("site's web frontend should have no LAN address and its Worker %q: %q", tc.site, site)
			}
		})
	}
}
