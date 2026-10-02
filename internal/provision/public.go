package provision

import (
	"context"
	"fmt"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"strconv"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

// CloudflareAPI is the subset of cloudflare.Client the orchestrator uses
// to publish a project's services.
type CloudflareAPI interface {
	FindTunnel(ctx context.Context, name string) (*cloudflare.Tunnel, error)
	SetRoutes(ctx context.Context, tunnelID string, hostnames []string, routes []cloudflare.Route) error
	EnsureTunnelRecord(ctx context.Context, hostname, tunnelID string) error
	DeleteRecord(ctx context.Context, hostname string) error
	DeleteWorker(ctx context.Context, name, hostname string) error
}

// PublicHostname is where a service of the project is published in an
// environment: <service>-<project>-urbex.<domain> in prod,
// <service>-staging-<project>-urbex.<domain> in staging, and the web
// frontend at <project>-urbex.<domain> and staging-<project>-urbex.<domain>
// (flat names; dots with nested ones). Empty when the platform publishes
// nothing.
func (o *Orchestrator) PublicHostname(env string, svc manifest.Service) string {
	if !o.Config.Cloudflare.Enabled() {
		return ""
	}
	// A web frontend is the project's own name; a service is prefixed
	// with its name.
	var labels []string
	if !svc.IsWeb() {
		labels = append(labels, svc.Name)
	}
	if env != "prod" {
		labels = append(labels, env)
	}
	labels = append(labels, o.Manifest.Project)
	return o.Config.Cloudflare.Hostname(o.Config.Domain, labels...)
}

// publish routes the environment's public services through the
// platform's tunnel and points their DNS records at it; services that
// aren't (or no longer are) public lose theirs.
func (o *Orchestrator) publish(ctx context.Context, env string, allocations map[string]allocation.Entry) error {
	if !o.Config.Cloudflare.Enabled() {
		return nil
	}
	if o.Cloudflare == nil {
		return fmt.Errorf("urbex.platform.yaml sets cloudflare.accountId but there is no Cloudflare token (URBEX_CLOUDFLARE_TOKEN)")
	}
	tunnel, err := o.tunnel(ctx)
	if err != nil {
		return err
	}
	var hostnames []string
	var routes []cloudflare.Route
	var private []string
	for _, svc := range o.Manifest.Services {
		host := o.PublicHostname(env, svc)
		hostnames = append(hostnames, host)
		if !svc.Public {
			private = append(private, host)
			continue
		}
		entry := allocations[Hostname(o.Manifest.Project, svc.Name, env)]
		routes = append(routes, cloudflare.Route{Hostname: host, Service: "http://" + entry.IP + ":" + strconv.Itoa(svc.Port)})
	}
	if err := o.Cloudflare.SetRoutes(ctx, tunnel.ID, hostnames, routes); err != nil {
		return err
	}
	for _, r := range routes {
		if err := o.Cloudflare.EnsureTunnelRecord(ctx, r.Hostname, tunnel.ID); err != nil {
			return err
		}
	}
	for _, host := range private {
		if err := o.Cloudflare.DeleteRecord(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

// unpublish removes the environment's services from the tunnel and DNS.
func (o *Orchestrator) unpublish(ctx context.Context, env string, services []manifest.Service) error {
	if !o.Config.Cloudflare.Enabled() {
		return nil
	}
	if o.Cloudflare == nil {
		return fmt.Errorf("urbex.platform.yaml sets cloudflare.accountId but there is no Cloudflare token (URBEX_CLOUDFLARE_TOKEN)")
	}
	tunnel, err := o.tunnel(ctx)
	if err != nil {
		return err
	}
	var hostnames []string
	for _, svc := range services {
		hostnames = append(hostnames, o.PublicHostname(env, svc))
	}
	if err := o.Cloudflare.SetRoutes(ctx, tunnel.ID, hostnames, nil); err != nil {
		return err
	}
	for _, host := range hostnames {
		if err := o.Cloudflare.DeleteRecord(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) tunnel(ctx context.Context) (*cloudflare.Tunnel, error) {
	tunnel, err := o.Cloudflare.FindTunnel(ctx, o.Config.Cloudflare.TunnelName)
	if err != nil {
		return nil, err
	}
	if tunnel == nil {
		return nil, fmt.Errorf("the platform's Cloudflare tunnel %s doesn't exist: run 'urbex bootstrap'", o.Config.Cloudflare.TunnelName)
	}
	return tunnel, nil
}

// unpublishWeb deletes the environment's web frontend Worker and its
// custom domain.
func (o *Orchestrator) unpublishWeb(ctx context.Context, env string) error {
	if !o.Config.Cloudflare.Enabled() {
		return nil
	}
	for _, svc := range o.Manifest.Deployables() {
		if !svc.IsWeb() {
			continue
		}
		if o.Cloudflare == nil {
			return fmt.Errorf("urbex.platform.yaml sets cloudflare.accountId but there is no Cloudflare token (URBEX_CLOUDFLARE_TOKEN)")
		}
		if err := o.Cloudflare.DeleteWorker(ctx, rollout.WorkerName(o.Manifest.Project, env), o.PublicHostname(env, svc)); err != nil {
			return err
		}
	}
	return nil
}
