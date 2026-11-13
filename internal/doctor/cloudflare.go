package doctor

import (
	"context"
	"slices"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/platform"
)

// CloudflareReader is what the Cloudflare checks read (cloudflare.Client).
type CloudflareReader interface {
	VerifyToken(ctx context.Context) (string, error)
	Zones(ctx context.Context) ([]cloudflare.Zone, error)
}

// CloudflareChecks checks the token and the zone when the platform
// config has a Cloudflare account; c is nil without a token.
func CloudflareChecks(ctx context.Context, c CloudflareReader, cfg *platform.Config, r *Report) {
	add := func(name, status, detail, fix string) {
		r.add(Check{Group: "cloudflare", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	switch {
	case cfg == nil:
		return
	case cfg.Cloudflare.QuickTunnel && !cfg.Cloudflare.Enabled():
		add("account", Skip, "a Quick Tunnel: no account or token needed", "")
		return
	case !cfg.Cloudflare.Enabled():
		add("account", Skip, "no Cloudflare in the platform config: everything stays on the LAN", "")
		return
	case c == nil:
		add("token", Fail, "the platform config has a Cloudflare account and there is no token",
			"create the account token (docs/reference/credentials.md#cloudflare-api-token) and store it with 'urbex-op set cloudflareToken'")
		return
	}
	status, err := c.VerifyToken(ctx)
	if err != nil || status != "active" {
		detail := "status " + status
		if err != nil {
			detail = err.Error()
		}
		add("token", Fail, detail, "check the token in the Cloudflare dashboard, or create a new one and store it with 'urbex-op set cloudflareToken'")
		return
	}
	add("token", OK, "active", "")
	zones, err := c.Zones(ctx)
	switch {
	case err != nil:
		add("zone", Fail, err.Error(), "the token needs Zone Read on the zone - see docs/reference/credentials.md#cloudflare-api-token")
	case !slices.ContainsFunc(zones, func(z cloudflare.Zone) bool { return z.ID == cfg.Cloudflare.ZoneID }):
		add("zone", Fail, "the token sees no zone with cloudflare.zoneId "+cfg.Cloudflare.ZoneID, "set cloudflare.zoneId to the zone of "+cfg.Domain+" ('urbex discover' lists the zones), or give the token access to it")
	default:
		for _, z := range zones {
			if z.ID == cfg.Cloudflare.ZoneID {
				status := OK
				detail := z.Name
				if z.Name != cfg.Domain {
					status, detail = Warn, z.Name+", but domain is "+cfg.Domain
				}
				add("zone", status, detail, "")
			}
		}
	}
}
