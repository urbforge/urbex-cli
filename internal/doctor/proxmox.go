package doctor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// ProxmoxReader is what the Proxmox checks read (proxmoxapi.Client).
type ProxmoxReader interface {
	Version(ctx context.Context) (string, error)
	Nodes(ctx context.Context) ([]string, error)
	Pools(ctx context.Context) ([]string, error)
	Permissions(ctx context.Context) (map[string]map[string]bool, error)
	Storages(ctx context.Context, node string) ([]proxmoxapi.Storage, error)
	Volumes(ctx context.Context, node, storage, content string) ([]string, error)
	Bridges(ctx context.Context, node string) ([]proxmoxapi.Bridge, error)
}

// Privileges urbex's token needs, where it needs them: on the pool, to
// create and manage the LXCs and VMs in it; on the storage, to give them
// disks; on the bridge, to attach them (docs/reference/credentials.md).
var (
	poolPrivileges    = []string{"VM.Allocate", "VM.Clone", "VM.Config.Disk", "VM.Config.Network", "VM.PowerMgmt", "VM.Audit"}
	storagePrivileges = []string{"Datastore.AllocateSpace"}
	bridgePrivileges  = []string{"SDN.Use"}
)

const credentialsDoc = "see docs/reference/credentials.md#proxmox-api-token (or the setup's prepare-proxmox.py)"

// ProxmoxChecks checks the node with the platform config's token: it
// answers, the node, pool, storage, bridge and templates exist, and the
// token has the privileges urbex needs. p is nil without a token.
func ProxmoxChecks(ctx context.Context, p ProxmoxReader, cfg *platform.Config, r *Report) {
	add := func(name, status, detail, fix string) {
		r.add(Check{Group: "proxmox", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	if cfg == nil {
		add("node", Skip, "no platform config to read the node from", "")
		return
	}
	if p == nil {
		add("API", Skip, "no Proxmox token", "store it with 'urbex-op set proxmoxToken'")
		return
	}
	pc := cfg.Proxmox
	version, err := p.Version(ctx)
	if err != nil {
		fix := "check proxmox.apiUrl (it ends in /api2/json), port 8006 and the network"
		if strings.Contains(err.Error(), "401") {
			fix = "the token is refused: check it is user@realm!name=secret, and not expired or deleted; store it again with 'urbex-op set proxmoxToken'"
		}
		add("API", Fail, fmt.Sprintf("%s: %v", pc.APIURL, err), fix)
		return
	}
	add("API", OK, "Proxmox VE "+version+" at "+pc.APIURL, "")

	nodes, err := p.Nodes(ctx)
	switch {
	case err != nil:
		add("node", Fail, err.Error(), "")
	case !slices.Contains(nodes, pc.Node):
		add("node", Fail, fmt.Sprintf("no node %q among %s", pc.Node, strings.Join(nodes, ", ")), "set proxmox.node to one of them")
	default:
		add("node", OK, pc.Node, "")
	}

	if pc.Pool != "" {
		pools, err := p.Pools(ctx)
		switch {
		case err != nil:
			add("pool", Fail, err.Error(), "")
		case !slices.Contains(pools, pc.Pool):
			add("pool", Fail, fmt.Sprintf("the token sees no pool %q", pc.Pool), "create the pool and give the token's group PVEVMAdmin, PVEDatastoreUser and PVEPoolUser on it - "+credentialsDoc)
		default:
			add("pool", OK, pc.Pool, "")
		}
	}

	perms, err := p.Permissions(ctx)
	if err != nil {
		add("privileges", Fail, err.Error(), credentialsDoc)
	} else {
		scope := "/vms"
		if pc.Pool != "" {
			scope = "/pool/" + pc.Pool
		}
		checks := []struct {
			path, what string
			privs      []string
		}{
			{scope, "the machines", poolPrivileges},
			{"/storage/" + pc.StoragePool, "storage " + pc.StoragePool, storagePrivileges},
			{"/sdn/zones/localnetwork/" + bridge(cfg), "bridge " + bridge(cfg), bridgePrivileges},
		}
		var missing []string
		for _, c := range checks {
			for _, priv := range c.privs {
				if !has(perms, c.path, priv) {
					missing = append(missing, priv+" on "+c.path)
				}
			}
		}
		if len(missing) > 0 {
			add("privileges", Fail, "the token lacks "+strings.Join(missing, ", "), credentialsDoc)
		} else {
			add("privileges", OK, "on "+scope+", storage "+pc.StoragePool+" and bridge "+bridge(cfg), "")
		}
	}

	storages, err := p.Storages(ctx, pc.Node)
	if err != nil {
		add("storage", Fail, err.Error(), "")
		return
	}
	byName := map[string]proxmoxapi.Storage{}
	for _, s := range storages {
		byName[s.Name] = s
	}
	if s, ok := byName[pc.StoragePool]; !ok {
		add("storage", Fail, fmt.Sprintf("the token sees no storage %q on %s", pc.StoragePool, pc.Node), "set proxmox.storagePool to a storage with the rootdir and images content types ('urbex discover' lists them)")
	} else if !s.Has("rootdir") || !s.Has("images") {
		add("storage", Fail, fmt.Sprintf("%s holds %s, not rootdir and images", s.Name, strings.Join(s.Content, ",")), "choose a storage for container and VM disks")
	} else {
		add("storage", OK, fmt.Sprintf("%s (%s), %d of %d GB free", s.Name, s.Type, s.AvailGB, s.TotalGB), "")
	}

	volumeCheck(ctx, p, pc.Node, byName, "LXC template", pc.LXCTemplate, "vztmpl",
		"download it on the node: 'pveam update && pveam download local debian-13-standard_<version>_amd64.tar.zst' ('pveam available' lists them), then set proxmox.lxcTemplate", r)
	bridges, err := p.Bridges(ctx, pc.Node)
	switch {
	case err != nil:
		add("bridge", Warn, err.Error(), "")
	case !slices.ContainsFunc(bridges, func(b proxmoxapi.Bridge) bool { return b.Name == bridge(cfg) }):
		add("bridge", Fail, fmt.Sprintf("no bridge %q on %s", bridge(cfg), pc.Node), "set proxmox.network.bridge to one of the node's bridges ('urbex discover' lists them)")
	default:
		add("bridge", OK, bridge(cfg), "")
	}
}

// volumeCheck checks that a volume (storage:content/file) is on the node.
func volumeCheck(ctx context.Context, p ProxmoxReader, node string, storages map[string]proxmoxapi.Storage, name, volid, content, fix string, r *Report) {
	add := func(status, detail, fix string) {
		r.add(Check{Group: "proxmox", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	storage, _, ok := strings.Cut(volid, ":")
	if !ok {
		add(Fail, fmt.Sprintf("%q is not a volume (storage:%s/file)", volid, content), fix)
		return
	}
	if _, ok := storages[storage]; !ok {
		add(Fail, fmt.Sprintf("the token sees no storage %q", storage), fix)
		return
	}
	vols, err := p.Volumes(ctx, node, storage, content)
	if err != nil {
		add(Fail, err.Error(), fix)
		return
	}
	if !slices.Contains(vols, volid) {
		detail := volid + " is not on " + node
		if len(vols) > 0 {
			detail += "; there is " + strings.Join(vols, ", ")
		}
		add(Fail, detail, fix)
		return
	}
	add(OK, volid, "")
}

func bridge(cfg *platform.Config) string {
	if cfg.Proxmox.Network.Bridge == "" {
		return "vmbr0"
	}
	return cfg.Proxmox.Network.Bridge
}

// has reports whether the token has priv on path. Proxmox lists the
// effective privileges at each path an ACL is on, and an ACL propagates
// down: a token with an ACL on / (root@pam) has them everywhere.
func has(perms map[string]map[string]bool, path, priv string) bool {
	for {
		if perms[path][priv] {
			return true
		}
		if path == "/" || path == "" {
			return false
		}
		i := strings.LastIndex(path, "/")
		path = path[:i]
		if path == "" {
			path = "/"
		}
	}
}
