package proxmoxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// --- the applications' volumes (ADR-0028) ----------------------------------

// MountPoint is one of an LXC's mount points (mp0 to mp255): a volume on
// a Proxmox storage, or a folder of the node bound into it (Volume is
// then the folder's path).
type MountPoint struct {
	Key      string // "mp0"
	Volume   string // "local-lvm:vm-9101-disk-1", or "/mnt/pve/nas/media"
	Path     string // where the LXC sees it
	SizeGB   int    // 0 for a folder of the node
	ReadOnly bool
}

// Index is the number of the mount point's key: 3 for mp3.
func (m MountPoint) Index() int {
	n, _ := strconv.Atoi(strings.TrimPrefix(m.Key, "mp"))
	return n
}

var mountPointKey = regexp.MustCompile(`^mp[0-9]+$`)

// MountPoints returns an LXC's mount points, in the order of their keys.
func (c *Client) MountPoints(ctx context.Context, node string, vmid int) ([]MountPoint, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), nil)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	var out []MountPoint
	for key, v := range cfg {
		if s, ok := v.(string); ok && mountPointKey.MatchString(key) {
			out = append(out, ParseMountPoint(key, s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index() < out[j].Index() })
	return out, nil
}

// ParseMountPoint reads a mount point's value as Proxmox writes it, e.g.
// "local-lvm:vm-9101-disk-1,mp=/srv/volumes/data,backup=1,size=8G".
func ParseMountPoint(key, value string) MountPoint {
	parts := strings.Split(value, ",")
	m := MountPoint{Key: key}
	for _, p := range parts {
		k, v, found := strings.Cut(p, "=")
		switch {
		case !found && m.Volume == "":
			m.Volume = p
		case k == "volume":
			m.Volume = v
		case k == "mp":
			m.Path = v
		case k == "size":
			m.SizeGB = sizeGB(v)
		case k == "ro":
			m.ReadOnly = v == "1"
		}
	}
	return m
}

// sizeGB reads a Proxmox size ("8G", "512M", "1T") in GiB, rounded up.
func sizeGB(s string) int {
	if s == "" {
		return 0
	}
	unit := s[len(s)-1]
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil {
		n, _ = strconv.ParseFloat(s, 64)
		return int(n / (1 << 30))
	}
	switch unit {
	case 'T':
		n *= 1024
	case 'M':
		n /= 1024
	case 'K':
		n /= 1024 * 1024
	}
	gb := int(n)
	if float64(gb) < n {
		gb++
	}
	return gb
}

// AddVolume gives an LXC a new mount point key: a volume of sizeGB on
// storage, mounted at path and included in the LXC's backups.
func (c *Client) AddVolume(ctx context.Context, node string, vmid int, key, storage string, sizeGB int, path string) error {
	form := url.Values{key: {fmt.Sprintf("%s:%d,mp=%s,backup=1", storage, sizeGB, path)}}
	_, err := c.call(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), form)
	return err
}

// ResizeVolume grows an LXC's mount point key to sizeGB, and waits.
func (c *Client) ResizeVolume(ctx context.Context, node string, vmid int, key string, sizeGB int) error {
	form := url.Values{"disk": {key}, "size": {fmt.Sprintf("%dG", sizeGB)}}
	return c.task(ctx, node, http.MethodPut, fmt.Sprintf("/nodes/%s/lxc/%d/resize", node, vmid), form)
}

// Pending returns the keys of an LXC's configuration changed but not
// applied yet: a running LXC applies some changes only when it restarts.
func (c *Client) Pending(ctx context.Context, node string, vmid int) ([]string, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/lxc/%d/pending", node, vmid), nil)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Key     string `json:"key"`
		Pending any    `json:"pending"`
		Delete  any    `json:"delete"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if e.Pending != nil || e.Delete != nil {
			keys = append(keys, e.Key)
		}
	}
	return keys, nil
}

// RebootContainer restarts a running LXC, and waits.
func (c *Client) RebootContainer(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/reboot", node, vmid), nil)
}
