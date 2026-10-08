package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/platform"
)

// Lookup resolves a name to its addresses.
type Lookup func(ctx context.Context, host string) ([]string, error)

// TechnitiumLookup resolves names by asking Technitium at ip directly:
// the operator's machine needn't resolve the internal zone.
func TechnitiumLookup(ip string) Lookup {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ip, "53"))
		},
	}
	return func(ctx context.Context, host string) ([]string, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return r.LookupHost(ctx, host)
	}
}

// CheckAddresses compares the addresses urbex computes for the platform's
// services (from urbex.platform.yaml, and uses from this machine) with
// what their internal names resolve to: an LXC recreated on another
// address, or a config that no longer matches the platform, would
// otherwise only fail later and further away (ADR-0027). A name pointing
// elsewhere is an error; Technitium not answering, or a name with no
// record yet (a platform not bootstrapped since), only a warning.
func CheckAddresses(ctx context.Context, cfg *platform.Config, lookup Lookup) (warning string, err error) {
	ips, _, err := allocateIPs(cfg)
	if err != nil {
		return "", err
	}
	var mismatched, missing []string
	for _, l := range platform.PlatformLabels {
		want := ips[Hostname(l.Service)]
		if want == "" {
			continue
		}
		name := cfg.InternalName(l.Label)
		got, err := lookup(ctx, name)
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			missing = append(missing, name)
		case err != nil:
			return fmt.Sprintf("could not check the platform's addresses with Technitium (%v): using %s", err, strings.Join(addressesOf(ips), ", ")), nil
		case !slices.Contains(got, want):
			mismatched = append(mismatched, fmt.Sprintf("%s is %s, but urbex.platform.yaml makes it %s", name, strings.Join(got, ","), want))
		}
	}
	if len(mismatched) > 0 {
		return "", fmt.Errorf("the platform's addresses don't match its internal DNS: %s - check proxmox.network in urbex.platform.yaml, or re-run 'urbex bootstrap'", strings.Join(mismatched, "; "))
	}
	if len(missing) > 0 {
		return "no internal DNS record for " + strings.Join(missing, ", ") + " - run 'urbex bootstrap' to create them", nil
	}
	return "", nil
}

func addressesOf(ips map[string]string) []string {
	var out []string
	for _, s := range BaseServices {
		if ip := ips[Hostname(s.Name)]; ip != "" {
			out = append(out, s.Name+"="+ip)
		}
	}
	return out
}
