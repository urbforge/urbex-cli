package doctor

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// DevicesReader is what the devices check reads of an LXC
// (proxmoxapi.Client).
type DevicesReader interface {
	Devices(ctx context.Context, node string, vmid int) ([]proxmoxapi.Device, error)
}

// DeviceMachine is a machine of the project in urbex.yaml whose
// applications declare devices, in one environment: its VMID, 0 when it
// hasn't been created yet, and the devices of all its applications.
type DeviceMachine struct {
	Service string // the application, or its group (lxc:)
	Env     string
	VMID    int
	VM      bool
	Devices []string
}

// DevicesChecks checks that each application's LXC has the device files
// it declares, which the operator binds as root@pam: one check per
// application and environment, with the pct set commands that are
// missing. d is nil without the Proxmox API.
func DevicesChecks(ctx context.Context, d DevicesReader, node string, machines []DeviceMachine, r *Report) {
	for _, m := range machines {
		add := func(status, detail, fix string) {
			r.add(Check{Group: "devices", Name: m.Service + " (" + m.Env + ")", Status: status, Detail: detail, Fix: fix})
		}
		want := strings.Join(m.Devices, ", ")
		switch {
		case m.VM:
			add(OK, want+": a VM has its own, nothing to bind", "")
			continue
		case m.VMID == 0:
			add(Skip, "its LXC isn't created yet", "'urbex apply "+m.Env+"' creates it and prints the commands for "+want)
			continue
		case d == nil:
			add(Skip, "needs the Proxmox API: a platform config and proxmoxToken", "")
			continue
		}
		have, err := d.Devices(ctx, node, m.VMID)
		if err != nil {
			add(Warn, "couldn't read the devices of LXC "+fmt.Sprint(m.VMID)+": "+firstLine(err.Error()), "")
			continue
		}
		taken := map[int]bool{}
		bound := map[string]bool{}
		for _, dev := range have {
			taken[dev.Index()] = true
			bound[path.Clean(dev.Path)] = true
		}
		var missing, commands []string
		next := 0
		for _, dev := range m.Devices {
			if bound[dev] {
				continue
			}
			for taken[next] {
				next++
			}
			taken[next] = true
			missing = append(missing, dev)
			commands = append(commands, fmt.Sprintf("pct set %d -dev%d %s", m.VMID, next, dev))
		}
		if len(missing) == 0 {
			add(OK, want+" bound to LXC "+fmt.Sprint(m.VMID), "")
			continue
		}
		add(Fail, "LXC "+fmt.Sprint(m.VMID)+" doesn't have "+strings.Join(missing, ", "),
			"on the node, as root: "+strings.Join(commands, "; ")+"; then 'urbex apply "+m.Env+"' deploys the application")
	}
}
