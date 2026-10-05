package bootstrap

import (
	"context"
	"fmt"

	"github.com/urbforge/urbex-cli/internal/credentials"
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
}

// setupDNS mints the platform's Technitium API token (once; re-running
// bootstrap keeps the existing one) and creates the zone internal
// records are written to: the platform's own public domain, used
// split-horizon - Cloudflare resolves it publicly, Technitium resolves
// it on the LAN. See
// docs/decisions/0026-technitium-internal-dns-records.md in
// urbforge/urbex.
func (o *Orchestrator) setupDNS(ctx context.Context, clients Clients, ep Endpoints) error {
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
	if err := build(ep.TechnitiumURL, creds.TechnitiumToken).EnsureZone(ctx, o.Config.Domain); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Technitium ready at %s (zone %s).\n", ep.TechnitiumURL, o.Config.Domain)
	return nil
}
