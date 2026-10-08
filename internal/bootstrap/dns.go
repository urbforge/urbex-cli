package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/technitium"
)

// TechnitiumAdminUser is Technitium's built-in admin account, given a
// password by the technitium Ansible role (DNS_SERVER_ADMIN_PASSWORD).
const TechnitiumAdminUser = "admin"

// TechnitiumAPI is the subset of technitium.Client bootstrap uses to set
// up the platform's internal DNS.
type TechnitiumAPI interface {
	CreateToken(ctx context.Context, user, pass, name string) (string, error)
	EnsureZone(ctx context.Context, zone string) error
	DeleteZone(ctx context.Context, zone string) error
	SetForwarders(ctx context.Context, servers []string) error
	SetRecord(ctx context.Context, zone, domain, ip string) error
}

// setupDNS mints the platform's Technitium API token (once; re-running
// bootstrap keeps the existing one), creates the internal zone - the
// platform's own subdomain - with forwarders for every other name, and
// gives every platform service its records. A platform set up before
// ADR-0027 had its zone named after the whole domain: its project
// records are recreated in the new zone, then it is deleted. See
// docs/decisions/0027-service-conventions.md in urbforge/urbex.
func (o *Orchestrator) setupDNS(ctx context.Context, clients Clients, ep Endpoints, ips map[string]string, gitopsRepoPath string) error {
	build := clients.Technitium
	if build == nil {
		build = func(u, t string) TechnitiumAPI { return technitium.New(u, t) }
	}
	creds := o.Credentials
	if creds.TechnitiumToken == "" {
		token, err := build(ep.TechnitiumURL, "").CreateToken(ctx, TechnitiumAdminUser, creds.TechnitiumAdminPassword, "urbex-cli")
		if err != nil {
			return err
		}
		if err := creds.Update(func(c *credentials.Credentials) { c.TechnitiumToken = token }); err != nil {
			return fmt.Errorf("saving technitium token: %w", err)
		}
	}
	dns := build(ep.TechnitiumURL, creds.TechnitiumToken)
	zone := o.Config.InternalZone()
	if err := dns.EnsureZone(ctx, zone); err != nil {
		return err
	}
	if err := dns.SetForwarders(ctx, o.Config.DNSForwarders()); err != nil {
		return err
	}
	for name, ip := range PlatformRecords(o.Config, ips) {
		if err := dns.SetRecord(ctx, zone, name, ip); err != nil {
			return err
		}
	}
	if err := o.migrateProjectRecords(ctx, dns, zone, gitopsRepoPath); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Technitium ready at %s (zone %s, forwarding to %s).\n", ep.TechnitiumURL, zone, strings.Join(o.Config.DNSForwarders(), ", "))
	return nil
}

// PlatformRecords maps every platform service's internal name to its
// LXC's address, ips being the base services' by LXC hostname. Names of
// services the platform doesn't run (the tunnel, without Cloudflare) are
// left out.
func PlatformRecords(cfg *platform.Config, ips map[string]string) map[string]string {
	records := map[string]string{}
	for _, l := range platform.PlatformLabels {
		if ip := ips[Hostname(l.Service)]; ip != "" {
			records[cfg.InternalName(l.Label)] = ip
		}
	}
	return records
}

// migrateProjectRecords recreates, in the internal zone, the record of
// every project service the GitOps repo deploys, then deletes the zone
// ADR-0026 named after the whole domain. Nothing to do when that zone
// is the internal one (dns.zone set to the domain).
func (o *Orchestrator) migrateProjectRecords(ctx context.Context, dns TechnitiumAPI, zone, gitopsRepoPath string) error {
	old := o.Config.Domain
	if old == zone {
		return nil
	}
	deployments, err := envrepo.Repo{Path: gitopsRepoPath}.Deployments()
	if err != nil {
		return err
	}
	ledger, err := allocation.Load(filepath.Join(gitopsRepoPath, "state", "allocations.json"), 0, 0)
	if err != nil {
		return err
	}
	for _, d := range deployments {
		for _, svc := range d.Services {
			// The LXC's hostname, as provision.Hostname names it. Web
			// frontends have no LXC, so no allocation and no record.
			entry, ok := ledger.Lookup(d.Project + "-" + svc + "-" + d.Env)
			if !ok || entry.IP == "" {
				continue
			}
			if err := dns.SetRecord(ctx, zone, o.Config.ServiceInternalName(d.Project, d.Env, svc), entry.IP); err != nil {
				return err
			}
		}
	}
	return dns.DeleteZone(ctx, old)
}
