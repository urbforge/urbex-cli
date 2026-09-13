package bootstrap

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

// Hostname returns the LXC hostname for a base service, e.g. "gitea" ->
// "urbex-gitea".
func Hostname(serviceName string) string {
	return "urbex-" + serviceName
}
