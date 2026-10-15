// Package proxmoxapi is a minimal Proxmox VE API client. It exists only
// to support urbex bootstrap's pre-flight check for already-existing
// base-service containers (see docs/decisions/0017-embedded-base-infra-assets.md
// in urbforge/urbex) - it does not attempt to replace Terraform's Proxmox
// provider, which does the actual provisioning.
package proxmoxapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to a Proxmox VE API endpoint using an API token
// (user@realm!tokenid=secret format, as returned by Proxmox when a token
// is created).
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New creates a Client. baseURL is typically
// "https://<host>:8006/api2/json". insecure skips TLS certificate
// verification, for nodes using Proxmox's default self-signed certificate
// (mirrors proxmox.insecure in urbex.platform.yaml).
func New(baseURL, token string, insecure bool) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via platform config
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second, Transport: transport},
	}
}

type container struct {
	Name     string `json:"name"`
	VMID     vmid   `json:"vmid"`
	Template any    `json:"template"`
}

// vmid accepts both forms Proxmox uses for a VMID: a number or a string.
type vmid int

func (v *vmid) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(bytes.Trim(b, `"`), &n); err != nil {
		return fmt.Errorf("vmid %s: %w", b, err)
	}
	*v = vmid(n)
	return nil
}

type listContainersResponse struct {
	Data []container `json:"data"`
}

// ListContainerHostnames returns the hostnames ("name" field) of every
// LXC container on the given Proxmox node that the token can see.
func (c *Client) ListContainerHostnames(ctx context.Context, node string) ([]string, error) {
	cts, err := c.listContainers(ctx, node)
	if err != nil {
		return nil, err
	}
	hostnames := make([]string, 0, len(cts))
	for _, ct := range cts {
		hostnames = append(hostnames, ct.Name)
	}
	return hostnames, nil
}

// ListContainerIDs returns the VMIDs of every LXC container on the given
// Proxmox node that the token can see.
func (c *Client) ListContainerIDs(ctx context.Context, node string) ([]int, error) {
	cts, err := c.listContainers(ctx, node)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(cts))
	for _, ct := range cts {
		ids = append(ids, int(ct.VMID))
	}
	return ids, nil
}

func (c *Client) listContainers(ctx context.Context, node string) ([]container, error) {
	url := fmt.Sprintf("%s/nodes/%s/lxc", c.baseURL, node)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling proxmox api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("proxmox api returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed listContainersResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding proxmox api response: %w", err)
	}
	return parsed.Data, nil
}

// --- what urbex's LXC template needs (redemptor#18) -----------------------

// ContainerSpec is a new LXC: from a container template (vztmpl), in a
// pool, with a static address.
type ContainerSpec struct {
	VMID         int
	Hostname     string
	OSTemplate   string // e.g. local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst
	Storage      string
	DiskGB       int
	Pool         string
	Bridge       string
	IPCIDR       string // e.g. 192.168.1.209/24
	Gateway      string
	Nameservers  []string
	SSHPublicKey string
	Keyctl       bool
}

// ContainerInfo is what urbex reads of an LXC's configuration.
type ContainerInfo struct {
	Template    bool
	Description string
}

// CreateContainer creates the LXC spec describes and waits for it.
func (c *Client) CreateContainer(ctx context.Context, node string, spec ContainerSpec) error {
	form := url.Values{}
	form.Set("vmid", fmt.Sprint(spec.VMID))
	form.Set("hostname", spec.Hostname)
	form.Set("ostemplate", spec.OSTemplate)
	form.Set("rootfs", fmt.Sprintf("%s:%d", spec.Storage, spec.DiskGB))
	form.Set("unprivileged", "1")
	features := "nesting=1"
	if spec.Keyctl {
		features += ",keyctl=1"
	}
	form.Set("features", features)
	form.Set("net0", fmt.Sprintf("name=eth0,bridge=%s,ip=%s,gw=%s", spec.Bridge, spec.IPCIDR, spec.Gateway))
	if len(spec.Nameservers) > 0 {
		form.Set("nameserver", strings.Join(spec.Nameservers, " "))
	}
	form.Set("ssh-public-keys", spec.SSHPublicKey)
	form.Set("memory", "2048")
	form.Set("cores", "2")
	if spec.Pool != "" {
		form.Set("pool", spec.Pool)
	}
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc", node), form)
}

// StartContainer starts an LXC and waits for it.
func (c *Client) StartContainer(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/start", node, vmid), nil)
}

// StopContainer stops an LXC and waits for it.
func (c *Client) StopContainer(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/stop", node, vmid), nil)
}

// ConvertToTemplate turns a stopped LXC into a template, the source of
// clones.
func (c *Client) ConvertToTemplate(ctx context.Context, node string, vmid int) error {
	_, err := c.call(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/template", node, vmid), nil)
	return err
}

// SetDescription sets an LXC's (or template's) description.
func (c *Client) SetDescription(ctx context.Context, node string, vmid int, description string) error {
	form := url.Values{"description": {description}}
	_, err := c.call(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), form)
	return err
}

// Container returns an LXC's configuration, or nil when there is no LXC
// vmid on node.
func (c *Client) Container(ctx context.Context, node string, vmid int) (*ContainerInfo, error) {
	ids, err := c.ListContainerIDs(ctx, node)
	if err != nil {
		return nil, err
	}
	found := false
	for _, id := range ids {
		found = found || id == vmid
	}
	if !found {
		return nil, nil
	}
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid), nil)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Template    any    `json:"template"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &ContainerInfo{Template: fmt.Sprint(cfg.Template) == "1", Description: strings.TrimSpace(cfg.Description)}, nil
}

// DeleteContainer destroys an LXC (or template) and its disks, and waits.
func (c *Client) DeleteContainer(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodDelete, fmt.Sprintf("/nodes/%s/lxc/%d?destroy-unreferenced-disks=1&purge=1", node, vmid), nil)
}

// call does an API request and returns its "data".
func (c *Client) call(ctx context.Context, method, path string, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling proxmox api: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxmox api %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding proxmox api response: %w", err)
	}
	return parsed.Data, nil
}

// TaskPollInterval is how often task waits a Proxmox task's status.
var TaskPollInterval = 2 * time.Second

// task does a request that starts a Proxmox task (its data is a UPID),
// then waits for the task to end and fails if it didn't end OK.
func (c *Client) task(ctx context.Context, node, method, path string, form url.Values) error {
	data, err := c.call(ctx, method, path, form)
	if err != nil {
		return err
	}
	var upid string
	if err := json.Unmarshal(data, &upid); err != nil || upid == "" {
		return nil // not a task: done already
	}
	for {
		status, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid)), nil)
		if err != nil {
			return err
		}
		var st struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := json.Unmarshal(status, &st); err != nil {
			return err
		}
		if st.Status == "stopped" {
			if st.ExitStatus != "OK" {
				return fmt.Errorf("proxmox task %s %s ended: %s", method, path, st.ExitStatus)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(TaskPollInterval):
		}
	}
}

// Templates returns the VMIDs of the node's LXC templates in [from, to].
func (c *Client) Templates(ctx context.Context, node string, from, to int) ([]int, error) {
	cts, err := c.listContainers(ctx, node)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, ct := range cts {
		if id := int(ct.VMID); id >= from && id <= to && fmt.Sprint(ct.Template) == "1" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
