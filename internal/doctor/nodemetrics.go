package doctor

import (
	"context"
	"strings"

	"github.com/urbforge/urbex-cli/internal/platform"
)

// PermissionsReader is what the node metrics check reads with the
// monitoring token (proxmoxapi.Client).
type PermissionsReader interface {
	Permissions(ctx context.Context) (map[string]map[string]bool, error)
}

// nodeMetricsPrivileges are what pve-exporter reads on / : the node's
// status and its storages (PVEAuditor has both).
var nodeMetricsPrivileges = []string{"Sys.Audit", "Datastore.Audit"}

// NodeMetricsChecks checks the optional read-only token for the Proxmox
// node's metrics (urbforge/urbex-cli#94). m is nil without the token:
// the node metrics are off, which is fine.
func NodeMetricsChecks(ctx context.Context, m PermissionsReader, cfg *platform.Config, r *Report) {
	add := func(status, detail, fix string) {
		r.add(Check{Group: "proxmox", Name: "node metrics", Status: status, Detail: detail, Fix: fix})
	}
	if cfg == nil {
		return
	}
	if m == nil {
		add(Skip, "off: no proxmoxMonitorToken, so Grafana shows the LXCs but not the node's CPU, memory and storages",
			"optional: create a read-only token (docs/reference/credentials.md#proxmox-monitoring-token), 'urbex-op set proxmoxMonitorToken', then 'urbex bootstrap'")
		return
	}
	perms, err := m.Permissions(ctx)
	if err != nil {
		add(Fail, "the monitoring token doesn't work: "+firstLine(err.Error()),
			"check proxmoxMonitorToken (user@realm!name=secret) and that the token exists on the node")
		return
	}
	var missing []string
	for _, p := range nodeMetricsPrivileges {
		if !perms["/"][p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		add(Fail, "the monitoring token lacks "+strings.Join(missing, ", ")+" on /",
			"on the node, as root: pveum acl modify / --users <its user> --roles PVEAuditor (and --tokens <the token> if it is privilege-separated)")
		return
	}
	add(OK, "read-only token with Sys.Audit and Datastore.Audit on /; 'urbex bootstrap' runs pve-exporter", "")
}
