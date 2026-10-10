package platform

import (
	"path"
	"strings"
)

// The service catalog (ADR-0027, urbex-cli#15): every service Urbex runs
// declares here how it follows the platform's conventions - its kind,
// where it runs, how Komodo deploys it, what it provides, and each of its
// endpoints with its internal name and exposure. Internal DNS names,
// Cloudflare tunnel routes and Access applications are generated from it,
// and a test fails when a service - a catalog entry, or a container in
// any compose file - doesn't declare them. Adding a service means adding
// it here.

// Kind is what a service is, the same on Komodo (tags) and in Grafana
// (the kind label).
type Kind string

const (
	KindPlatform Kind = "platform" // the platform's services, created by Urbex
	KindApp      Kind = "app"      // the users' applications
	KindAgent    Kind = "agent"    // technical agents running next to another service
)

// Exposure is who can reach an endpoint.
type Exposure string

const (
	// ExposurePublic: on the internet, through the platform's tunnel.
	ExposurePublic Exposure = "public"
	// ExposureAccess: through the tunnel, behind Cloudflare Access - the
	// admin and internal UIs and APIs.
	ExposureAccess Exposure = "access"
	// ExposureInternal: by internal name only, never published - what only
	// other services use (DNS on port 53, write paths, databases).
	ExposureInternal Exposure = "internal"
)

// Deploy is how a service is deployed.
type Deploy string

const (
	// DeployFilesOnHost: a Komodo Stack from compose files on its server,
	// installed by Ansible - the bootstrap services, which Komodo can't
	// depend on to deploy them.
	DeployFilesOnHost Deploy = "komodo-files-on-host"
	// DeployGitOps: a Komodo Stack from the GitOps repo.
	DeployGitOps Deploy = "komodo-gitops"
	// DeployAnsible: by Ansible only - Periphery, the agent Komodo
	// deploys through, and the Quick Tunnel sidecar.
	DeployAnsible Deploy = "ansible"
)

// Endpoint is one way to reach a service.
type Endpoint struct {
	// Label is the internal DNS label (<label>.<zone>), and the public
	// hostname's when the endpoint is published.
	Label string
	Port  int
	// Path narrows the endpoint to a path: a regex for a tunnel route
	// ("^/listener/"), a prefix for an Access application ("/admin").
	Path     string
	Exposure Exposure
}

// Service is a catalog entry.
type Service struct {
	// Name is the service, as its containers' urbex.service label says.
	Name string
	Kind Kind
	// LXC is the base service whose LXC it runs on ("observability");
	// empty for an agent, which runs on every LXC.
	LXC    string
	Deploy Deploy
	// Provides are the capabilities it gives the platform: what a similar
	// service replacing it would have to provide (redemptor#16).
	Provides []string
	// Endpoints, or OutboundOnly for a service that listens on nothing:
	// it connects out (Periphery to Komodo, the tunnel to Cloudflare).
	Endpoints    []Endpoint
	OutboundOnly bool
}

// Catalog is every platform service and agent. A project's services are
// AppService entries.
var Catalog = []Service{
	{Name: "technitium", Kind: KindPlatform, LXC: "technitium", Deploy: DeployFilesOnHost, Provides: []string{"dns"},
		Endpoints: []Endpoint{
			{Label: "dns", Port: 5380, Exposure: ExposureAccess},
			{Label: "dns", Port: 53, Exposure: ExposureInternal},
		}},
	{Name: "gitea", Kind: KindPlatform, LXC: "gitea", Deploy: DeployFilesOnHost, Provides: []string{"git", "registry"},
		Endpoints: []Endpoint{
			{Label: "git", Port: 3000, Exposure: ExposureAccess},
		}},
	{Name: "komodo", Kind: KindPlatform, LXC: "komodo", Deploy: DeployFilesOnHost, Provides: []string{"ci", "cd"},
		Endpoints: []Endpoint{
			{Label: "komodo", Port: 9120, Exposure: ExposureAccess},
			// Only the webhook listener: every call is signed.
			{Label: "hooks", Port: 9120, Path: "^/listener/", Exposure: ExposurePublic},
		}},
	{Name: "keycloak", Kind: KindPlatform, LXC: "keycloak", Deploy: DeployGitOps, Provides: []string{"identity"},
		Endpoints: []Endpoint{
			// The projects' realms are public; the admin console and the
			// admin realm aren't.
			{Label: "auth", Port: 8080, Exposure: ExposurePublic},
			{Label: "auth", Port: 8080, Path: "/admin", Exposure: ExposureAccess},
			{Label: "auth", Port: 8080, Path: "/realms/master", Exposure: ExposureAccess},
		}},
	{Name: "grafana", Kind: KindPlatform, LXC: "observability", Deploy: DeployGitOps, Provides: []string{"dashboards"},
		Endpoints: []Endpoint{
			{Label: "grafana", Port: 3000, Exposure: ExposureAccess},
		}},
	{Name: "prometheus", Kind: KindPlatform, LXC: "observability", Deploy: DeployGitOps, Provides: []string{"metrics"},
		Endpoints: []Endpoint{
			{Label: "prometheus", Port: 9090, Exposure: ExposureAccess},
			{Label: "prometheus", Port: 9090, Path: "^/api/v1/(write|otlp)", Exposure: ExposureInternal},
		}},
	{Name: "loki", Kind: KindPlatform, LXC: "observability", Deploy: DeployGitOps, Provides: []string{"logs"},
		Endpoints: []Endpoint{
			{Label: "loki", Port: 3100, Exposure: ExposureAccess},
			{Label: "loki", Port: 3100, Path: "^/(loki/api/v1/push|api/prom/push|otlp/)", Exposure: ExposureInternal},
		}},
	{Name: "tunnel", Kind: KindPlatform, LXC: "tunnel", Deploy: DeployGitOps, Provides: []string{"ingress"},
		// cloudflared connects out to Cloudflare; its name is reserved.
		Endpoints:    []Endpoint{{Label: "tunnel", Exposure: ExposureInternal}},
		OutboundOnly: true},
	{Name: "periphery", Kind: KindAgent, Deploy: DeployAnsible, Provides: []string{"deploy-agent"}, OutboundOnly: true},
	{Name: "alloy", Kind: KindAgent, Deploy: DeployGitOps, Provides: []string{"telemetry-agent"}, OutboundOnly: true},
	{Name: "quicktunnel", Kind: KindAgent, Deploy: DeployAnsible, Provides: []string{"ingress"}, OutboundOnly: true},
}

// CatalogService returns the catalog entry named name.
func CatalogService(name string) (Service, bool) {
	for _, s := range Catalog {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// AppService is the catalog entry of a project's service: an app,
// deployed from the GitOps repo, public through the tunnel with
// public: true, internal otherwise. Its internal name is computed per
// environment (Config.ServiceInternalName), not a label of the zone.
func AppService(name string, port int, public bool) Service {
	exposure := ExposureInternal
	if public {
		exposure = ExposurePublic
	}
	return Service{Name: name, Kind: KindApp, Deploy: DeployGitOps, Provides: []string{"application"},
		Endpoints: []Endpoint{{Label: name, Port: port, Exposure: exposure}}}
}

// Published are the catalog's endpoints that get a public hostname -
// public, or behind Access - in catalog order.
func Published() []Endpoint {
	var out []Endpoint
	for _, s := range Catalog {
		for _, e := range s.Endpoints {
			if e.Exposure == ExposurePublic || e.Exposure == ExposureAccess {
				out = append(out, e)
			}
		}
	}
	return out
}

// AccessAppSuffix names an Access application for e: its label, plus the
// last segment of its path when it covers one ("auth-admin").
func (e Endpoint) AccessAppSuffix() string {
	if e.Path == "" {
		return e.Label
	}
	return e.Label + "-" + path.Base(strings.TrimRight(e.Path, "/"))
}

// labels are the internal DNS labels of the platform's services, each
// with the base service whose LXC answers it, in catalog order.
func labels() []PlatformLabel {
	var out []PlatformLabel
	seen := map[string]bool{}
	for _, s := range Catalog {
		for _, e := range s.Endpoints {
			if e.Label != "" && !seen[e.Label] && s.LXC != "" {
				seen[e.Label] = true
				out = append(out, PlatformLabel{e.Label, s.LXC})
			}
		}
	}
	return out
}
