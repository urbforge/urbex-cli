// Package proxmoxapi is a minimal Proxmox VE API client. It exists only
// to support urbex bootstrap's pre-flight check for already-existing
// base-service containers (see docs/decisions/0017-embedded-base-infra-assets.md
// in urbforge/urbex) - it does not attempt to replace Terraform's Proxmox
// provider, which does the actual provisioning.
package proxmoxapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		http:    &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}
}

type container struct {
	Name string `json:"name"`
}

type listContainersResponse struct {
	Data []container `json:"data"`
}

// ListContainerHostnames returns the hostnames ("name" field) of every
// LXC container on the given Proxmox node.
func (c *Client) ListContainerHostnames(ctx context.Context, node string) ([]string, error) {
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

	hostnames := make([]string, 0, len(parsed.Data))
	for _, ct := range parsed.Data {
		hostnames = append(hostnames, ct.Name)
	}
	return hostnames, nil
}
