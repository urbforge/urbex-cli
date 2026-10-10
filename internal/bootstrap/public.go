package bootstrap

import (
	"context"
	"fmt"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/platform"
)

// CloudflareAPI is the subset of cloudflare.Client used to publish the
// platform's endpoints.
type CloudflareAPI interface {
	FindTunnel(ctx context.Context, name string) (*cloudflare.Tunnel, error)
	EnsureTunnel(ctx context.Context, name string) (*cloudflare.Tunnel, error)
	TunnelToken(ctx context.Context, tunnelID string) (string, error)
	DeleteTunnel(ctx context.Context, tunnelID string) error
	SetRoutes(ctx context.Context, tunnelID string, hostnames []string, routes []cloudflare.Route) error
	EnsureTunnelRecord(ctx context.Context, hostname, tunnelID string) error
	DeleteRecord(ctx context.Context, hostname string) error
	EnsureEmailPolicy(ctx context.Context, name string, emails []string) (string, error)
	DeletePolicy(ctx context.Context, name string) error
	EnsureAccessApp(ctx context.Context, name, hostname, policyID string) error
	DeleteAccessApp(ctx context.Context, name string) error
	HasOneTimePIN(ctx context.Context) (bool, error)
	EnableOneTimePIN(ctx context.Context) error
}

// PublicEndpoints are the platform's public hostnames (ADR-0022): the
// services' public APIs and UIs, and - behind Cloudflare Access - their
// admin and internal ones (ADR-0027).
type PublicEndpoints struct {
	Keycloak, Gitea, Komodo, Hooks string
	DNS, Grafana, Prometheus, Loki string
	hosts                          []string
}

// PublicEndpointsFor returns the platform's public hostnames, or the zero
// value when it doesn't publish any.
func PublicEndpointsFor(cfg *platform.Config) PublicEndpoints {
	if !cfg.Cloudflare.Enabled() {
		return PublicEndpoints{}
	}
	cf := cfg.Cloudflare
	return PublicEndpoints{
		Keycloak: cf.Hostname(cfg.Domain, "auth"),
		Gitea:    cf.Hostname(cfg.Domain, "git"),
		Komodo:   cf.Hostname(cfg.Domain, "komodo"),
		Hooks:    cf.Hostname(cfg.Domain, "hooks"),

		DNS:        cf.Hostname(cfg.Domain, "dns"),
		Grafana:    cf.Hostname(cfg.Domain, "grafana"),
		Prometheus: cf.Hostname(cfg.Domain, "prometheus"),
		Loki:       cf.Hostname(cfg.Domain, "loki"),
		hosts:      publishedHosts(cfg),
	}
}

// all are the platform's public hostnames: those of the service
// catalog's published endpoints, public or behind Access.
func (p PublicEndpoints) all() []string {
	return p.hosts
}

// publishedHosts are the hostnames of the catalog's published labels, in
// catalog order.
func publishedHosts(cfg *platform.Config) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, e := range platform.Published() {
		if !seen[e.Label] {
			seen[e.Label] = true
			hosts = append(hosts, cfg.Cloudflare.Hostname(cfg.Domain, e.Label))
		}
	}
	return hosts
}

// accessApp is a Cloudflare Access application urbex creates: Name is
// the suffix of its name, Domain the hostname - or hostname and path -
// it protects.
type accessApp struct{ Name, Domain string }

// accessApps are the catalog's endpoints behind Access (ADR-0027) - the
// admin and internal UIs and APIs - each an Access application on its
// hostname, and path for a part of a service (Keycloak's /admin).
func accessApps(cfg *platform.Config) []accessApp {
	var apps []accessApp
	for _, e := range platform.Published() {
		if e.Exposure == platform.ExposureAccess {
			apps = append(apps, accessApp{e.AccessAppSuffix(), cfg.Cloudflare.Hostname(cfg.Domain, e.Label) + e.Path})
		}
	}
	return apps
}

// tunnelRoutes are the catalog's routes through the platform's tunnel, to
// the services' internal names: first the paths that stay internal on a
// published hostname - answering 404, only the agents use them, inside -
// so that they win over the hostname's own route; then every public
// endpoint, and every Access one that is a whole hostname.
func tunnelRoutes(cfg *platform.Config) []cloudflare.Route {
	published := map[string]bool{}
	for _, e := range platform.Published() {
		published[e.Label] = true
	}
	var blocked, routes []cloudflare.Route
	for _, s := range platform.Catalog {
		for _, e := range s.Endpoints {
			host := cfg.Cloudflare.Hostname(cfg.Domain, e.Label)
			switch {
			case e.Exposure == platform.ExposureInternal && e.Path != "" && published[e.Label]:
				blocked = append(blocked, cloudflare.Route{Hostname: host, Path: e.Path, Service: "http_status:404"})
			case e.Exposure == platform.ExposurePublic || (e.Exposure == platform.ExposureAccess && e.Path == ""):
				route := cloudflare.Route{Hostname: host, Service: fmt.Sprintf("http://%s:%d", cfg.InternalName(e.Label), e.Port)}
				if e.Exposure == platform.ExposurePublic {
					route.Path = e.Path
				}
				routes = append(routes, route)
			}
		}
	}
	return append(blocked, routes...)
}

// Names of the Access objects Urbex creates (prefixed with the
// platform's name).
func accessPolicyName(cfg *platform.Config) string { return cfg.Cloudflare.Name + "-operators" }
func accessAppName(cfg *platform.Config, service string) string {
	return cfg.Cloudflare.Name + "-" + service
}

func (o *Orchestrator) cloudflare(clients Clients) CloudflareAPI {
	if clients.Cloudflare != nil {
		return clients.Cloudflare(o.Credentials.CloudflareToken, o.Config.Cloudflare.AccountID, o.Config.Cloudflare.ZoneID)
	}
	return cloudflare.New(cloudflare.DefaultBaseURL, o.Credentials.CloudflareToken, o.Config.Cloudflare.AccountID, o.Config.Cloudflare.ZoneID)
}

// tunnelToken ensures the platform's tunnel exists and returns the token
// its connector runs with.
func (o *Orchestrator) tunnelToken(ctx context.Context, clients Clients) (string, error) {
	cf := o.cloudflare(clients)
	tunnel, err := cf.EnsureTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil {
		return "", err
	}
	return cf.TunnelToken(ctx, tunnel.ID)
}

// publishPlatform puts the admin and internal UIs behind Cloudflare Access, then
// routes the platform's public hostnames through its tunnel and points
// their DNS records at it. In that order: a protected service must never
// be reachable before its Access application exists, so if Access can't
// be set up nothing is published.
func (o *Orchestrator) publishPlatform(ctx context.Context, clients Clients) error {
	cf := o.cloudflare(clients)
	pub := PublicEndpointsFor(o.Config)

	if err := o.ensureOneTimePIN(ctx, cf); err != nil {
		return err
	}
	policy, err := cf.EnsureEmailPolicy(ctx, accessPolicyName(o.Config), o.Config.Cloudflare.Access.Emails)
	if err != nil {
		return accessPermission(err)
	}
	for _, app := range accessApps(o.Config) {
		if err := cf.EnsureAccessApp(ctx, accessAppName(o.Config, app.Name), app.Domain, policy); err != nil {
			return accessPermission(err)
		}
	}

	tunnel, err := cf.EnsureTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil {
		return err
	}
	// cloudflared reaches the services by internal name (ADR-0027).
	routes := tunnelRoutes(o.Config)
	if err := cf.SetRoutes(ctx, tunnel.ID, pub.all(), routes); err != nil {
		return err
	}
	for _, h := range pub.all() {
		if err := cf.EnsureTunnelRecord(ctx, h, tunnel.ID); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.Out, "Published on Cloudflare: Keycloak https://%s (its admin console behind Access), webhooks https://%s;\n", pub.Keycloak, pub.Hooks)
	fmt.Fprintf(o.Out, "behind Access: Gitea https://%s, Komodo https://%s, Technitium https://%s, Grafana https://%s, Prometheus https://%s, Loki https://%s.\n",
		pub.Gitea, pub.Komodo, pub.DNS, pub.Grafana, pub.Prometheus, pub.Loki)
	return nil
}

// ensureOneTimePIN makes sure Access can log people in: with a one-time
// PIN sent to the allowed e-mail addresses.
func (o *Orchestrator) ensureOneTimePIN(ctx context.Context, cf CloudflareAPI) error {
	ok, err := cf.HasOneTimePIN(ctx)
	if cloudflare.Forbidden(err) {
		fmt.Fprintln(o.Out, "Warning: the Cloudflare token can't read Zero Trust login methods; make sure One-time PIN is enabled (Zero Trust > Settings > Authentication), or nobody can log in through Access (Gitea, Komodo, Grafana, ...).")
		return nil
	}
	if err != nil || ok {
		return err
	}
	if err := cf.EnableOneTimePIN(ctx); err != nil {
		if cloudflare.Forbidden(err) {
			return fmt.Errorf("Access has no login method and the Cloudflare token can't add one: enable One-time PIN in Zero Trust > Settings > Authentication, then run 'urbex bootstrap' again")
		}
		return err
	}
	fmt.Fprintln(o.Out, "Enabled the one-time PIN login for Cloudflare Access.")
	return nil
}

// unpublishPlatform removes what publishPlatform created, and the tunnel.
func (o *Orchestrator) unpublishPlatform(ctx context.Context, clients Clients) error {
	cf := o.cloudflare(clients)
	pub := PublicEndpointsFor(o.Config)
	for _, app := range accessApps(o.Config) {
		if err := cf.DeleteAccessApp(ctx, accessAppName(o.Config, app.Name)); err != nil {
			return err
		}
	}
	if err := cf.DeletePolicy(ctx, accessPolicyName(o.Config)); err != nil {
		return err
	}
	for _, h := range pub.all() {
		if err := cf.DeleteRecord(ctx, h); err != nil {
			return err
		}
	}
	tunnel, err := cf.FindTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil || tunnel == nil {
		return err
	}
	return cf.DeleteTunnel(ctx, tunnel.ID)
}

// accessPermission explains an Access call refused for lack of permission.
func accessPermission(err error) error {
	if cloudflare.Forbidden(err) {
		return fmt.Errorf("%w - the Cloudflare token needs the permission Access: Apps and Policies (Edit); nothing was published", err)
	}
	return err
}
