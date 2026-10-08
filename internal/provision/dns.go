package provision

import (
	"context"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

// TechnitiumAPI is the subset of technitium.Client the orchestrator uses
// to give a project's services internal DNS records.
type TechnitiumAPI interface {
	SetRecord(ctx context.Context, zone, domain, ip string) error
	DeleteRecord(ctx context.Context, zone, domain, ip string) error
}

// InternalHostname is where a service of the project resolves inside the
// platform via Technitium: <service>.<env>.<project> under the internal
// zone, the nested form of its public hostname (ADR-0027), whether or
// not Cloudflare is configured.
func (o *Orchestrator) InternalHostname(env string, svc manifest.Service) string {
	return o.Config.ServiceInternalName(o.Manifest.Project, env, svc.Name)
}

// registerDNS gives every service of the environment an internal DNS
// record on Technitium, pointing at its allocated IP. Web frontends have
// no LXC (they deploy to a Cloudflare Worker) and so get none. See
// docs/decisions/0026-technitium-internal-dns-records.md and
// docs/decisions/0027-service-conventions.md in urbforge/urbex.
func (o *Orchestrator) registerDNS(ctx context.Context, env string, allocations map[string]allocation.Entry) error {
	if o.Technitium == nil {
		return nil
	}
	for _, svc := range o.Manifest.Services {
		entry := allocations[Hostname(o.Manifest.Project, svc.Name, env)]
		if err := o.Technitium.SetRecord(ctx, o.Config.InternalZone(), o.InternalHostname(env, svc), entry.IP); err != nil {
			return err
		}
	}
	return nil
}

// unregisterDNS removes the internal DNS records of services, the
// environment's services being destroyed.
func (o *Orchestrator) unregisterDNS(ctx context.Context, env string, services []manifest.Service, allocations map[string]allocation.Entry) error {
	if o.Technitium == nil {
		return nil
	}
	for _, svc := range services {
		entry := allocations[Hostname(o.Manifest.Project, svc.Name, env)]
		if err := o.Technitium.DeleteRecord(ctx, o.Config.InternalZone(), o.InternalHostname(env, svc), entry.IP); err != nil {
			return err
		}
	}
	return nil
}
