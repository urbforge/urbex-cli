package proxmoxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Device is a device file of the node passed to an LXC (dev0 to dev255).
// Only root@pam can add one to an unprivileged LXC: the operator does,
// and urbex reads it.
type Device struct {
	Key  string // "dev0"
	Path string // "/dev/net/tun"
}

// Index is the number of the device's key: 3 for dev3.
func (d Device) Index() int {
	n, _ := strconv.Atoi(strings.TrimPrefix(d.Key, "dev"))
	return n
}

var deviceKey = regexp.MustCompile(`^dev[0-9]+$`)

// Devices returns the device files passed to an LXC, in the order of
// their keys.
func (c *Client) Devices(ctx context.Context, node string, vmid int) ([]Device, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), nil)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	var out []Device
	for key, v := range cfg {
		if s, ok := v.(string); ok && deviceKey.MatchString(key) {
			out = append(out, ParseDevice(key, s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index() < out[j].Index() })
	return out, nil
}

// ParseDevice reads a device's value as Proxmox writes it, e.g.
// "/dev/net/tun" or "path=/dev/dri/renderD128,gid=104".
func ParseDevice(key, value string) Device {
	d := Device{Key: key}
	for _, p := range strings.Split(value, ",") {
		k, v, found := strings.Cut(p, "=")
		switch {
		case !found && d.Path == "":
			d.Path = p
		case k == "path":
			d.Path = v
		}
	}
	return d
}
