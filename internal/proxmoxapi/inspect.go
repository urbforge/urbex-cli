package proxmoxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// --- reading the node: urbex doctor and urbex discover (ADR-0032) ----------
//
// Everything here works with a token scoped to the platform's pool: it
// reads what that token may see, and changes nothing.

// Version is the Proxmox VE release, e.g. "9.2.21".
func (c *Client) Version(ctx context.Context) (string, error) {
	data, err := c.call(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Nodes returns the names of the cluster's nodes the token sees.
func (c *Client) Nodes(ctx context.Context) ([]string, error) {
	data, err := c.call(ctx, http.MethodGet, "/nodes", nil)
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Node)
	}
	sort.Strings(out)
	return out, nil
}

// Pools returns the resource pools the token sees.
func (c *Client) Pools(ctx context.Context) ([]string, error) {
	data, err := c.call(ctx, http.MethodGet, "/pools", nil)
	if err != nil {
		return nil, err
	}
	var pools []struct {
		PoolID string `json:"poolid"`
	}
	if err := json.Unmarshal(data, &pools); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pools))
	for _, p := range pools {
		out = append(out, p.PoolID)
	}
	sort.Strings(out)
	return out, nil
}

// Permissions returns the token's privileges, by path ("/pool/urbex",
// "/storage/local-zfs", "/sdn/zones/localnetwork/vmbr0", ...).
func (c *Client) Permissions(ctx context.Context) (map[string]map[string]bool, error) {
	data, err := c.call(ctx, http.MethodGet, "/access/permissions", nil)
	if err != nil {
		return nil, err
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]bool, len(raw))
	for path, privs := range raw {
		out[path] = make(map[string]bool, len(privs))
		for p := range privs {
			out[path][p] = true
		}
	}
	return out, nil
}

// Storage is one of a node's storages.
type Storage struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Content []string `json:"content"`
	AvailGB int      `json:"availGB"`
	TotalGB int      `json:"totalGB"`
	Active  bool     `json:"active"`
}

// Has reports whether the storage holds content of a type (rootdir,
// images, vztmpl, import, ...).
func (s Storage) Has(content string) bool {
	for _, c := range s.Content {
		if c == content {
			return true
		}
	}
	return false
}

// Storages returns the node's storages the token sees, by name.
func (c *Client) Storages(ctx context.Context, node string) ([]Storage, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/storage", node), nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
		Content string `json:"content"`
		Avail   int64  `json:"avail"`
		Total   int64  `json:"total"`
		Active  int    `json:"active"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Storage, 0, len(raw))
	for _, s := range raw {
		var content []string
		for _, c := range strings.Split(s.Content, ",") {
			if c = strings.TrimSpace(c); c != "" {
				content = append(content, c)
			}
		}
		sort.Strings(content)
		out = append(out, Storage{Name: s.Storage, Type: s.Type, Content: content, AvailGB: int(s.Avail >> 30), TotalGB: int(s.Total >> 30), Active: s.Active == 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Volumes returns the volume IDs of a storage's content of a type
// ("vztmpl", "import", ...), e.g. "local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst".
func (c *Client) Volumes(ctx context.Context, node, storage, content string) ([]string, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/storage/%s/content?content=%s", node, storage, url.QueryEscape(content)), nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		VolID string `json:"volid"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.VolID)
	}
	sort.Strings(out)
	return out, nil
}

// Bridge is one of a node's Linux bridges.
type Bridge struct {
	Name    string `json:"name"`
	CIDR    string `json:"cidr,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

// Bridges returns the node's Linux bridges.
func (c *Client) Bridges(ctx context.Context, node string) ([]Bridge, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/network?type=bridge", node), nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Iface   string `json:"iface"`
		CIDR    string `json:"cidr"`
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Bridge, 0, len(raw))
	for _, b := range raw {
		out = append(out, Bridge{Name: b.Iface, CIDR: b.CIDR, Gateway: b.Gateway})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VMIDFree reports whether no machine on the cluster has vmid: Proxmox's
// nextid answers 400 for one that is taken.
func (c *Client) VMIDFree(ctx context.Context, vmid int) (bool, error) {
	_, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/cluster/nextid?vmid=%d", vmid), nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "400") {
		return false, nil
	}
	return false, err
}

// Member is a machine in a pool.
type Member struct {
	VMID int    `json:"vmid"`
	Name string `json:"name"`
	Type string `json:"type"` // lxc or qemu
}

// PoolMembers returns the LXCs and VMs of a pool.
func (c *Client) PoolMembers(ctx context.Context, pool string) ([]Member, error) {
	data, err := c.call(ctx, http.MethodGet, "/pools/"+url.PathEscape(pool), nil)
	if err != nil {
		return nil, err
	}
	var p struct {
		Members []struct {
			VMID vmid   `json:"vmid"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"members"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	var out []Member
	for _, m := range p.Members {
		if m.Type == "lxc" || m.Type == "qemu" {
			out = append(out, Member{VMID: int(m.VMID), Name: m.Name, Type: m.Type})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VMID < out[j].VMID })
	return out, nil
}
