package proxmoxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// What `urbex restore --from proxmox` needs (ADR-0036, point 3): the
// cluster's machines with their pool, a storage's backups of one machine,
// and the whole-machine restore onto the same VMID.

// Machine is one LXC or VM of the cluster, as /cluster/resources lists it.
type Machine struct {
	VMID     int
	Name     string
	Kind     string // lxc or qemu
	Node     string
	Pool     string
	Status   string // running, stopped
	Template bool
}

// Machines returns every LXC and VM of the cluster the token can see, with
// its pool (shown only to a token with Pool.Audit on it).
func (c *Client) Machines(ctx context.Context) ([]Machine, error) {
	data, err := c.call(ctx, http.MethodGet, "/cluster/resources?type=vm", nil)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		VMID     vmid   `json:"vmid"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Node     string `json:"node"`
		Pool     string `json:"pool"`
		Status   string `json:"status"`
		Template any    `json:"template"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decoding the cluster's machines: %w", err)
	}
	out := make([]Machine, 0, len(rows))
	for _, r := range rows {
		out = append(out, Machine{
			VMID: int(r.VMID), Name: r.Name, Kind: r.Type, Node: r.Node, Pool: r.Pool,
			Status: r.Status, Template: fmt.Sprint(r.Template) == "1",
		})
	}
	return out, nil
}

// Backup is one vzdump backup of a machine on a storage.
type Backup struct {
	VolID string // e.g. local:backup/vzdump-lxc-9000-2026_11_26-06_01_07.tar.zst
	VMID  int
	Kind  string // lxc or qemu
	Time  time.Time
	Size  int64
}

// Backups returns the backups of machine vmid on node's storage, oldest
// first.
func (c *Client) Backups(ctx context.Context, node, storage string, id int) ([]Backup, error) {
	path := fmt.Sprintf("/nodes/%s/storage/%s/content?content=backup&vmid=%d", node, url.PathEscape(storage), id)
	data, err := c.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		VolID   string `json:"volid"`
		VMID    vmid   `json:"vmid"`
		Subtype string `json:"subtype"`
		CTime   int64  `json:"ctime"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decoding the backups on %s: %w", storage, err)
	}
	out := make([]Backup, 0, len(rows))
	for _, r := range rows {
		// The vmid filter is the API's; check it anyway, a wrong backup
		// would replace the machine with another one.
		if int(r.VMID) != id {
			continue
		}
		out = append(out, Backup{VolID: r.VolID, VMID: int(r.VMID), Kind: r.Subtype, Time: time.Unix(r.CTime, 0), Size: r.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// StopMachine stops an LXC or a VM (kind lxc or qemu) and waits.
func (c *Client) StopMachine(ctx context.Context, node, kind string, id int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/%s/%d/status/stop", node, kind, id), nil)
}

// StartMachine starts an LXC or a VM and waits.
func (c *Client) StartMachine(ctx context.Context, node, kind string, id int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/%s/%d/status/start", node, kind, id), nil)
}

// RestoreMachine restores the backup volid onto machine id, replacing it
// (force): its configuration comes from the backup, its disks are created
// on storage. The machine must be stopped. It waits for the task.
func (c *Client) RestoreMachine(ctx context.Context, node, kind string, id int, volid, storage string) error {
	form := url.Values{}
	form.Set("vmid", fmt.Sprint(id))
	form.Set("force", "1")
	form.Set("storage", storage)
	switch kind {
	case "lxc":
		form.Set("ostemplate", volid)
		form.Set("restore", "1")
	case "qemu":
		form.Set("archive", volid)
	default:
		return fmt.Errorf("machine %d: unknown kind %q", id, kind)
	}
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/%s", node, kind), form)
}
