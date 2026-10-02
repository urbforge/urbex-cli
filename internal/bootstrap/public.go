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

// PublicEndpoints are the platform's public hostnames (ADR-0022).
type PublicEndpoints struct {
	Keycloak, Gitea, Komodo, Hooks string
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
	}
}

func (p PublicEndpoints) all() []string {
	return []string{p.Keycloak, p.Gitea, p.Komodo, p.Hooks}
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

// publishPlatform puts Gitea and Komodo behind Cloudflare Access, then
// routes the platform's public hostnames through its tunnel and points
// their DNS records at it. In that order: a protected service must never
// be reachable before its Access application exists, so if Access can't
// be set up nothing is published.
func (o *Orchestrator) publishPlatform(ctx context.Context, clients Clients, ips map[string]string) error {
	cf := o.cloudflare(clients)
	pub := PublicEndpointsFor(o.Config)

	if err := o.ensureOneTimePIN(ctx, cf); err != nil {
		return err
	}
	policy, err := cf.EnsureEmailPolicy(ctx, accessPolicyName(o.Config), o.Config.Cloudflare.Access.Emails)
	if err != nil {
		return err
	}
	for service, host := range map[string]string{"git": pub.Gitea, "komodo": pub.Komodo} {
		if err := cf.EnsureAccessApp(ctx, accessAppName(o.Config, service), host, policy); err != nil {
			return err
		}
	}

	tunnel, err := cf.EnsureTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil {
		return err
	}
	keycloak := "http://" + ips[Hostname("keycloak")] + ":8080"
	gitea := "http://" + ips[Hostname("gitea")] + ":3000"
	komodo := "http://" + ips[Hostname("komodo")] + ":9120"
	routes := []cloudflare.Route{
		{Hostname: pub.Keycloak, Service: keycloak},
		{Hostname: pub.Gitea, Service: gitea},
		{Hostname: pub.Komodo, Service: komodo},
		// Only Komodo's webhook listener: every call is signed.
		{Hostname: pub.Hooks, Path: "^/listener/", Service: komodo},
	}
	if err := cf.SetRoutes(ctx, tunnel.ID, pub.all(), routes); err != nil {
		return err
	}
	for _, h := range pub.all() {
		if err := cf.EnsureTunnelRecord(ctx, h, tunnel.ID); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.Out, "Published on Cloudflare: Keycloak https://%s, Gitea https://%s and Komodo https://%s (behind Access), webhooks https://%s.\n",
		pub.Keycloak, pub.Gitea, pub.Komodo, pub.Hooks)
	return nil
}

// ensureOneTimePIN makes sure Access can log people in: with a one-time
// PIN sent to the allowed e-mail addresses.
func (o *Orchestrator) ensureOneTimePIN(ctx context.Context, cf CloudflareAPI) error {
	ok, err := cf.HasOneTimePIN(ctx)
	if cloudflare.Forbidden(err) {
		fmt.Fprintln(o.Out, "Warning: the Cloudflare token can't read Zero Trust login methods; make sure One-time PIN is enabled (Zero Trust > Settings > Authentication), or nobody can log in to Gitea and Komodo through Access.")
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
	for _, service := range []string{"git", "komodo"} {
		if err := cf.DeleteAccessApp(ctx, accessAppName(o.Config, service)); err != nil {
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
