package bootstrap

import "github.com/urbforge/urbex-cli/internal/platform"

// Service describes one of the fixed base-service LXCs urbex bootstrap
// provisions. See docs/decisions/0009-one-lxc-per-service-per-env.md in
// urbforge/urbex.
type Service struct {
	// Name is the short identifier used in Terraform module names,
	// Ansible group names, and as the suffix of the LXC hostname.
	Name string
	Role string
}

// BaseServices is the fixed, ordered list of base-service LXCs. Order
// matters: it determines both the VMID offsets in
// platform/terraform/base/main.tf and the static IP allocation order in
// internal/netalloc.
var BaseServices = []Service{
	{Name: "gitea", Role: "Self-hosted Git server for app and GitOps repos"},
	{Name: "komodo", Role: "Build/update pipeline and LXC lifecycle management"},
	{Name: "technitium", Role: "Internal DNS"},
	{Name: "keycloak", Role: "Identity provider (SSO + OIDC)"},
	{Name: "observability", Role: "Prometheus + Grafana + Loki"},
}

// TunnelService runs the Cloudflare Tunnel connector (cloudflared). It is
// a base service only on a platform that publishes endpoints on
// Cloudflare, and comes last: VMID and IP offset 5. See
// docs/decisions/0022-cloudflare-tunnel-access-workers.md in urbforge/urbex.
var TunnelService = Service{Name: "tunnel", Role: "Cloudflare Tunnel connector (cloudflared)"}

// BaseServicesFor returns the base services of a platform: BaseServices,
// plus TunnelService when it publishes endpoints on Cloudflare.
func BaseServicesFor(cfg *platform.Config) []Service {
	services := append([]Service{}, BaseServices...)
	if cfg != nil && cfg.Cloudflare.Enabled() {
		services = append(services, TunnelService)
	}
	return services
}

// Hostname returns the LXC hostname for a base service, e.g. "gitea" ->
// "urbex-gitea".
func Hostname(serviceName string) string {
	return "urbex-" + serviceName
}
