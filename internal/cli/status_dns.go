package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/technitium"
)

// dnsChecker reads the internal DNS records status reports (ADR-0027),
// from Technitium itself - not the name registration would use, which
// would show one even where the record is missing.
type dnsChecker struct {
	client *technitium.Client
	zone   string
	down   error // the first failure: Technitium is not asked again
}

// newDNSChecker is nil when this machine has no Technitium token (the
// platform was bootstrapped without one): status then shows the names
// without checking them.
func newDNSChecker(cfg *platform.Config, creds *credentials.Credentials) *dnsChecker {
	if creds.TechnitiumToken == "" {
		return nil
	}
	ep, err := bootstrap.EndpointsFor(cfg)
	if err != nil {
		return nil
	}
	return &dnsChecker{client: technitium.New(ep.TechnitiumURL, creds.TechnitiumToken), zone: cfg.InternalZone()}
}

// Check is how a service's internal name reads in status: the name when
// its record points at ip, otherwise what is wrong with it. Safe on a
// nil checker, which shows the name unchecked.
func (d *dnsChecker) Check(ctx context.Context, host, ip string) string {
	if host == "" {
		return ""
	}
	if d == nil {
		return "dns=" + host + " (not checked: no technitium token)"
	}
	if d.down != nil {
		return "dns=" + host + " (technitium unreachable)"
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := d.client.GetRecords(qctx, d.zone, host)
	if err != nil {
		d.down = err
		return "dns=" + host + " (technitium: " + err.Error() + ")"
	}
	return "dns=" + dnsState(host, ip, ips)
}

// dnsState compares a name's records with the IP the ledger allocated.
func dnsState(host, want string, got []string) string {
	switch {
	case len(got) == 0:
		return host + " (missing)"
	case len(got) == 1 && got[0] == want:
		return host
	default:
		return fmt.Sprintf("%s (points to %s, expected %s)", host, strings.Join(got, ","), want)
	}
}
