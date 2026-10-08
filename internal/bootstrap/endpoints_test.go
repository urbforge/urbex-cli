package bootstrap

import (
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
)

func TestPlatformEndpoints(t *testing.T) {
	cfg := validConfig()
	eps, err := PlatformEndpoints(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]PlatformEndpoint{}
	for _, e := range eps {
		byName[e.Name] = e
	}
	if e := byName["Grafana"]; e.LAN != "http://192.168.1.204:3000" || e.Internal != "http://grafana.urbex.example.com:3000" || e.Public != "" {
		t.Errorf("Grafana = %+v", e)
	}
	if e := byName["Gitea (web, git, registry)"]; e.LAN != "http://192.168.1.200:3000" || e.Public != "" {
		t.Errorf("without Cloudflare nothing is public: %+v", e)
	}

	cfg.Domain = "cubotto.net"
	cfg.Cloudflare = platform.CloudflareConfig{AccountID: "a", ZoneID: "z", Name: "urbex", Hostnames: platform.HostnamesFlat}
	eps, _ = PlatformEndpoints(cfg)
	for _, e := range eps {
		byName[e.Name] = e
	}
	if e := byName["Komodo"]; e.Public != "https://komodo-urbex.cubotto.net" || e.Note == "" {
		t.Errorf("Komodo = %+v", e)
	}
	if e := byName["Komodo webhooks"]; e.Public != "https://hooks-urbex.cubotto.net/listener/" {
		t.Errorf("webhooks = %+v", e)
	}
	if e := byName["Keycloak"]; e.Public != "https://auth-urbex.cubotto.net" {
		t.Errorf("Keycloak = %+v", e)
	}
}
