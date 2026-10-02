package bootstrap

import "github.com/urbforge/urbex-cli/internal/platform"

// PlatformEndpoint is one address of a base service.
type PlatformEndpoint struct {
	Service string // base service (gitea, komodo, ...)
	Name    string // what answers there
	LAN     string // on the LAN, always
	Public  string // on the internet (with Cloudflare), or ""
	Note    string // how it is protected, what it is for
}

// PlatformEndpoints lists every address of the platform's base services,
// computed from the platform config: the LXCs' static addresses and, with
// Cloudflare, their public hostnames.
func PlatformEndpoints(cfg *platform.Config) ([]PlatformEndpoint, error) {
	ips, _, err := allocateIPs(cfg)
	if err != nil {
		return nil, err
	}
	pub := PublicEndpointsFor(cfg)
	lan := func(service, port string) string { return "http://" + ips[Hostname(service)] + ":" + port }
	https := func(host string) string {
		if host == "" {
			return ""
		}
		return "https://" + host
	}
	access := ""
	if cfg.Cloudflare.Enabled() {
		access = "public: behind Cloudflare Access"
	}
	return []PlatformEndpoint{
		{"gitea", "Gitea (web, git, registry)", lan("gitea", "3000"), https(pub.Gitea), access},
		{"komodo", "Komodo", lan("komodo", "9120"), https(pub.Komodo), access},
		{"komodo", "Komodo webhooks", lan("komodo", "9120") + "/listener/", httpsPath(pub.Hooks, "/listener/"), "signed calls only"},
		{"keycloak", "Keycloak", lan("keycloak", "8080"), https(pub.Keycloak), ""},
		{"technitium", "Technitium DNS console", lan("technitium", "5380"), "", ""},
		{"observability", "Grafana", lan("observability", "3000"), "", "dashboards Urbex logs, Urbex resources"},
		{"observability", "Prometheus", lan("observability", "9090"), "", ""},
		{"observability", "Loki", lan("observability", "3100"), "", ""},
	}, nil
}

func httpsPath(host, path string) string {
	if host == "" {
		return ""
	}
	return "https://" + host + path
}
