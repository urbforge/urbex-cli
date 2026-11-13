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

// dnsRecord is what status found of a service's internal name: State is
// ok, missing, elsewhere (PointsTo isn't Expected), not-checked (no
// Technitium token), unreachable or error (Detail says what failed).
type dnsRecord struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	PointsTo []string `json:"pointsTo,omitempty"`
	Expected string   `json:"expected,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// The states of a dnsRecord.
const (
	dnsOK          = "ok"
	dnsMissing     = "missing"
	dnsElsewhere   = "elsewhere"
	dnsNotChecked  = "not-checked"
	dnsUnreachable = "unreachable"
	dnsError       = "error"
)

// Lookup reads a service's internal name: whether its record points at
// ip. Safe on a nil checker, which leaves the name unchecked; nil for no
// name.
func (d *dnsChecker) Lookup(ctx context.Context, host, ip string) *dnsRecord {
	if host == "" {
		return nil
	}
	r := &dnsRecord{Name: host, Expected: ip}
	switch {
	case d == nil:
		r.State = dnsNotChecked
		return r
	case d.down != nil:
		r.State = dnsUnreachable
		return r
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := d.client.GetRecords(qctx, d.zone, host)
	if err != nil {
		d.down = err
		r.State, r.Detail = dnsError, err.Error()
		return r
	}
	r.PointsTo = ips
	switch {
	case len(ips) == 0:
		r.State = dnsMissing
	case len(ips) == 1 && ips[0] == ip:
		r.State = dnsOK
	default:
		r.State = dnsElsewhere
	}
	return r
}

// Check is how a service's internal name reads in status: the name when
// its record points at ip, otherwise what is wrong with it. Safe on a
// nil checker, which shows the name unchecked.
func (d *dnsChecker) Check(ctx context.Context, host, ip string) string {
	return d.Lookup(ctx, host, ip).String()
}

// String is the record as status prints it: "dns=<name>", with what is
// wrong in brackets; "" for no record.
func (r *dnsRecord) String() string {
	if r == nil {
		return ""
	}
	switch r.State {
	case dnsNotChecked:
		return "dns=" + r.Name + " (not checked: no technitium token)"
	case dnsUnreachable:
		return "dns=" + r.Name + " (technitium unreachable)"
	case dnsError:
		return "dns=" + r.Name + " (technitium: " + r.Detail + ")"
	}
	return "dns=" + dnsState(r.Name, r.Expected, r.PointsTo)
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
