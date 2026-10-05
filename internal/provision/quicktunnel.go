package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// QuickTunnelPort is where cloudflared's metrics server listens on a
// service's LXC when the quicktunnel Ansible role runs (see
// docs/decisions/0025-cloudflare-quick-tunnel.md in urbforge/urbex).
const QuickTunnelPort = 49399

// QuickTunnelLookup finds the https://*.trycloudflare.com hostname a
// service's Cloudflare Quick Tunnel sidecar was given. It is queried
// live rather than stored anywhere: a quick tunnel is handed a new
// hostname every time its container restarts.
type QuickTunnelLookup func(ctx context.Context, ip string) (string, error)

// QuickTunnelURL asks the cloudflared sidecar at ip for its quick
// tunnel's hostname, retrying every 500ms until ctx is done - the
// connector takes a few seconds to register with Cloudflare's edge
// after it starts, and the caller decides how long is worth waiting
// (longer right after 'urbex apply' starts it, short for 'urbex
// status').
func QuickTunnelURL(ctx context.Context, ip string) (string, error) {
	return quickTunnelURL(ctx, fmt.Sprintf("http://%s:%d/quicktunnel", ip, QuickTunnelPort))
}

// quickTunnelURL is QuickTunnelURL with the metrics endpoint's URL taken
// as a parameter, so tests can point it at an httptest.Server instead of
// the hardcoded QuickTunnelPort.
func quickTunnelURL(ctx context.Context, url string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}

	var lastErr error
	for {
		hostname, err := fetchQuickTunnelHostname(ctx, client, url)
		if err == nil {
			return "https://" + hostname, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("quick tunnel did not report a hostname in time: %w", lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func fetchQuickTunnelHostname(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Hostname == "" {
		return "", fmt.Errorf("not ready yet")
	}
	return out.Hostname, nil
}
