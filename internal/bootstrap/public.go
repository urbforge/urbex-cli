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
	}
}

func (p PublicEndpoints) all() []string {
	return []string{p.Keycloak, p.Gitea, p.Komodo, p.Hooks, p.DNS, p.Grafana, p.Prometheus, p.Loki}
}

// accessApp is a Cloudflare Access application urbex creates: Name is
// the suffix of its name, Domain the hostname - or hostname and path -
// it protects.
type accessApp struct{ Name, Domain string }

// accessApps are the platform's admin and internal UIs and APIs, behind
// Access (ADR-0027): Gitea's web, Komodo's UI, Technitium's console,
// Grafana, Prometheus, Loki, and Keycloak's admin console and admin
// realm - its other realms, the projects', stay public.
func (p PublicEndpoints) accessApps() []accessApp {
	return []accessApp{
		{"git", p.Gitea},
		{"komodo", p.Komodo},
		{"dns", p.DNS},
		{"grafana", p.Grafana},
		{"prometheus", p.Prometheus},
		{"loki", p.Loki},
		{"auth-admin", p.Keycloak + "/admin"},
		{"auth-master", p.Keycloak + "/realms/master"},
	}
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
	for _, app := range pub.accessApps() {
		if err := cf.EnsureAccessApp(ctx, accessAppName(o.Config, app.Name), app.Domain, policy); err != nil {
			return accessPermission(err)
		}
	}

	tunnel, err := cf.EnsureTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil {
		return err
	}
	// cloudflared reaches the services by internal name (ADR-0027).
	keycloak := "http://" + o.Config.InternalName("auth") + ":8080"
	gitea := "http://" + o.Config.InternalName("git") + ":3000"
	komodo := "http://" + o.Config.InternalName("komodo") + ":9120"
	notFound := "http_status:404"
	routes := []cloudflare.Route{
		{Hostname: pub.Keycloak, Service: keycloak},
		{Hostname: pub.Gitea, Service: gitea},
		{Hostname: pub.Komodo, Service: komodo},
		// Only Komodo's webhook listener: every call is signed.
		{Hostname: pub.Hooks, Path: "^/listener/", Service: komodo},
		{Hostname: pub.DNS, Service: "http://" + o.Config.InternalName("dns") + ":5380"},
		{Hostname: pub.Grafana, Service: "http://" + o.Config.InternalName("grafana") + ":3000"},
		// The write paths stay internal (ADR-0027): only the agents use
		// them, by internal name. The rules before a hostname's own win.
		{Hostname: pub.Prometheus, Path: "^/api/v1/(write|otlp)", Service: notFound},
		{Hostname: pub.Prometheus, Service: "http://" + o.Config.InternalName("prometheus") + ":9090"},
		{Hostname: pub.Loki, Path: "^/(loki/api/v1/push|api/prom/push|otlp/)", Service: notFound},
		{Hostname: pub.Loki, Service: "http://" + o.Config.InternalName("loki") + ":3100"},
	}
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
	for _, app := range pub.accessApps() {
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
