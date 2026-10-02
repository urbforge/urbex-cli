// Package cloudflare is a minimal client for the parts of Cloudflare's API
// Urbex uses: a remotely-managed tunnel and its ingress rules, DNS
// records, and Access applications. See
// docs/decisions/0022-cloudflare-tunnel-access-workers.md in urbforge/urbex.
//
// Everything Urbex creates is named with its prefix and found again by
// name: nothing else on the account is ever changed.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// ManagedComment marks the DNS records Urbex manages.
const ManagedComment = "managed by urbex"

// Client talks to one Cloudflare account and zone.
type Client struct {
	baseURL   string
	token     string
	accountID string
	zoneID    string
	http      *http.Client
}

// New returns a Client for the account and zone, authenticating with an
// API token.
func New(baseURL, token, accountID, zoneID string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		accountID: accountID,
		zoneID:    zoneID,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is an error Cloudflare reported.
type APIError struct {
	Status int
	Errors []apiErrorItem
}

// apiErrorItem is one error Cloudflare reported; some APIs (Access) put
// the reason in "error" rather than "message".
type apiErrorItem struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Err     string `json:"error"`
}

func (e *APIError) Error() string {
	var msgs []string
	for _, er := range e.Errors {
		msg := er.Message
		if msg == "" {
			msg = er.Err
		}
		msgs = append(msgs, fmt.Sprintf("%s (%d)", msg, er.Code))
	}
	if len(msgs) == 0 {
		msgs = append(msgs, http.StatusText(e.Status))
	}
	return "cloudflare: " + strings.Join(msgs, "; ")
}

// Forbidden reports whether err is Cloudflare refusing for lack of
// permission - a token missing a permission group.
func Forbidden(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusForbidden || apiErr.Status == http.StatusUnauthorized {
		return true
	}
	for _, e := range apiErr.Errors {
		if e.Code == 10000 || e.Code == 9109 || e.Code == 1010 || e.Err == "auth.forbidden" {
			return true
		}
	}
	return false
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling cloudflare api: %w", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []apiErrorItem  `json:"errors"`
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("cloudflare %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if !envelope.Success || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %w", method, path, &APIError{Status: resp.StatusCode, Errors: envelope.Errors})
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decoding cloudflare %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *Client) account(path string) string { return "/accounts/" + c.accountID + path }
func (c *Client) zone(path string) string    { return "/zones/" + c.zoneID + path }

// --- tunnel ------------------------------------------------------------

// Tunnel is a Cloudflare Tunnel.
type Tunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FindTunnel returns the (not deleted) tunnel with the name, or nil.
func (c *Client) FindTunnel(ctx context.Context, name string) (*Tunnel, error) {
	var tunnels []Tunnel
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	if err := c.do(ctx, http.MethodGet, c.account("/cfd_tunnel?"+q.Encode()), nil, &tunnels); err != nil {
		return nil, fmt.Errorf("looking up tunnel %s: %w", name, err)
	}
	for _, t := range tunnels {
		if t.Name == name {
			return &t, nil
		}
	}
	return nil, nil
}

// EnsureTunnel returns the tunnel with the name, creating it - remotely
// managed: its ingress rules are set through the API - if missing.
func (c *Client) EnsureTunnel(ctx context.Context, name string) (*Tunnel, error) {
	t, err := c.FindTunnel(ctx, name)
	if err != nil || t != nil {
		return t, err
	}
	var created Tunnel
	body := map[string]string{"name": name, "config_src": "cloudflare"}
	if err := c.do(ctx, http.MethodPost, c.account("/cfd_tunnel"), body, &created); err != nil {
		return nil, fmt.Errorf("creating tunnel %s: %w", name, err)
	}
	return &created, nil
}

// TunnelToken returns the token a cloudflared connector runs the tunnel
// with.
func (c *Client) TunnelToken(ctx context.Context, tunnelID string) (string, error) {
	var token string
	if err := c.do(ctx, http.MethodGet, c.account("/cfd_tunnel/"+tunnelID+"/token"), nil, &token); err != nil {
		return "", fmt.Errorf("reading the tunnel's token: %w", err)
	}
	return token, nil
}

// DeleteTunnel deletes the tunnel, dropping its connections first.
func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
	if err := c.do(ctx, http.MethodDelete, c.account("/cfd_tunnel/"+tunnelID+"/connections"), nil, nil); err != nil {
		return fmt.Errorf("dropping the tunnel's connections: %w", err)
	}
	if err := c.do(ctx, http.MethodDelete, c.account("/cfd_tunnel/"+tunnelID), nil, nil); err != nil {
		return fmt.Errorf("deleting the tunnel: %w", err)
	}
	return nil
}

// Route is one ingress rule: requests for Hostname (and Path, a regular
// expression, if set) go to Service, e.g. http://192.168.1.200:3000.
type Route struct {
	Hostname string `json:"hostname,omitempty"`
	Path     string `json:"path,omitempty"`
	Service  string `json:"service"`
	// OriginRequest is kept as it was on routes Urbex doesn't change.
	OriginRequest map[string]any `json:"originRequest,omitempty"`
}

// catchAll is the rule a tunnel's ingress must end with.
var catchAll = Route{Service: "http_status:404"}

// Routes returns the tunnel's ingress rules, without the final catch-all.
func (c *Client) Routes(ctx context.Context, tunnelID string) ([]Route, error) {
	var res struct {
		Config struct {
			Ingress []Route `json:"ingress"`
		} `json:"config"`
	}
	if err := c.do(ctx, http.MethodGet, c.account("/cfd_tunnel/"+tunnelID+"/configurations"), nil, &res); err != nil {
		return nil, fmt.Errorf("reading the tunnel's routes: %w", err)
	}
	var routes []Route
	for _, r := range res.Config.Ingress {
		if r.Hostname != "" {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

// SetRoutes replaces the routes for the given hostnames with routes (an
// empty routes removes them), keeping every other hostname's.
func (c *Client) SetRoutes(ctx context.Context, tunnelID string, hostnames []string, routes []Route) error {
	current, err := c.Routes(ctx, tunnelID)
	if err != nil {
		return err
	}
	replaced := map[string]bool{}
	for _, h := range hostnames {
		replaced[h] = true
	}
	var ingress []Route
	for _, r := range current {
		if !replaced[r.Hostname] {
			ingress = append(ingress, r)
		}
	}
	ingress = append(ingress, routes...)
	ingress = append(ingress, catchAll)
	body := map[string]any{"config": map[string]any{"ingress": ingress}}
	if err := c.do(ctx, http.MethodPut, c.account("/cfd_tunnel/"+tunnelID+"/configurations"), body, nil); err != nil {
		return fmt.Errorf("setting the tunnel's routes: %w", err)
	}
	return nil
}

// --- DNS ---------------------------------------------------------------

type dnsRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
	TTL     int    `json:"ttl,omitempty"`
}

func (c *Client) findRecord(ctx context.Context, name string) (*dnsRecord, error) {
	var records []dnsRecord
	q := url.Values{"name": {name}}
	if err := c.do(ctx, http.MethodGet, c.zone("/dns_records?"+q.Encode()), nil, &records); err != nil {
		return nil, fmt.Errorf("looking up DNS record %s: %w", name, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

// EnsureTunnelRecord points hostname at the tunnel with a proxied CNAME.
// A record of the name that Urbex didn't create is left alone, and
// reported.
func (c *Client) EnsureTunnelRecord(ctx context.Context, hostname, tunnelID string) error {
	want := dnsRecord{Type: "CNAME", Name: hostname, Content: tunnelID + ".cfargotunnel.com", Proxied: true, Comment: ManagedComment, TTL: 1}
	existing, err := c.findRecord(ctx, hostname)
	if err != nil {
		return err
	}
	if existing == nil {
		if err := c.do(ctx, http.MethodPost, c.zone("/dns_records"), want, nil); err != nil {
			return fmt.Errorf("creating DNS record %s: %w", hostname, err)
		}
		return nil
	}
	if existing.Comment != ManagedComment {
		return fmt.Errorf("DNS record %s already exists and wasn't created by urbex: remove or rename it", hostname)
	}
	if existing.Type == want.Type && existing.Content == want.Content && existing.Proxied {
		return nil
	}
	if err := c.do(ctx, http.MethodPut, c.zone("/dns_records/"+existing.ID), want, nil); err != nil {
		return fmt.Errorf("updating DNS record %s: %w", hostname, err)
	}
	return nil
}

// DeleteRecord deletes the DNS record of hostname if Urbex created it.
func (c *Client) DeleteRecord(ctx context.Context, hostname string) error {
	existing, err := c.findRecord(ctx, hostname)
	if err != nil || existing == nil || existing.Comment != ManagedComment {
		return err
	}
	if err := c.do(ctx, http.MethodDelete, c.zone("/dns_records/"+existing.ID), nil, nil); err != nil {
		return fmt.Errorf("deleting DNS record %s: %w", hostname, err)
	}
	return nil
}

// --- Access ------------------------------------------------------------

type accessPolicy struct {
	ID       string           `json:"id,omitempty"`
	Name     string           `json:"name"`
	Decision string           `json:"decision"`
	Include  []map[string]any `json:"include"`
}

// EnsureEmailPolicy creates or updates a reusable Access policy, named
// name, allowing exactly the e-mail addresses; it returns its ID.
func (c *Client) EnsureEmailPolicy(ctx context.Context, name string, emails []string) (string, error) {
	want := accessPolicy{Name: name, Decision: "allow"}
	for _, e := range emails {
		want.Include = append(want.Include, map[string]any{"email": map[string]string{"email": e}})
	}
	var policies []accessPolicy
	if err := c.do(ctx, http.MethodGet, c.account("/access/policies"), nil, &policies); err != nil {
		return "", fmt.Errorf("listing Access policies: %w", err)
	}
	for _, p := range policies {
		if p.Name == name {
			if err := c.do(ctx, http.MethodPut, c.account("/access/policies/"+p.ID), want, nil); err != nil {
				return "", fmt.Errorf("updating Access policy %s: %w", name, err)
			}
			return p.ID, nil
		}
	}
	var created accessPolicy
	if err := c.do(ctx, http.MethodPost, c.account("/access/policies"), want, &created); err != nil {
		return "", fmt.Errorf("creating Access policy %s: %w", name, err)
	}
	return created.ID, nil
}

// DeletePolicy deletes the reusable Access policy named name, if any.
func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	var policies []accessPolicy
	if err := c.do(ctx, http.MethodGet, c.account("/access/policies"), nil, &policies); err != nil {
		return fmt.Errorf("listing Access policies: %w", err)
	}
	for _, p := range policies {
		if p.Name == name {
			if err := c.do(ctx, http.MethodDelete, c.account("/access/policies/"+p.ID), nil, nil); err != nil {
				return fmt.Errorf("deleting Access policy %s: %w", name, err)
			}
		}
	}
	return nil
}

type accessApp struct {
	ID              string            `json:"id,omitempty"`
	Name            string            `json:"name"`
	Domain          string            `json:"domain"`
	Type            string            `json:"type"`
	SessionDuration string            `json:"session_duration"`
	Policies        []accessAppPolicy `json:"policies"`
}

type accessAppPolicy struct {
	ID         string `json:"id"`
	Precedence int    `json:"precedence"`
}

func (c *Client) accessApps(ctx context.Context) ([]accessApp, error) {
	var apps []accessApp
	if err := c.do(ctx, http.MethodGet, c.account("/access/apps"), nil, &apps); err != nil {
		return nil, fmt.Errorf("listing Access applications: %w", err)
	}
	return apps, nil
}

// EnsureAccessApp protects hostname with an Access application, named
// name, that the policy decides on.
func (c *Client) EnsureAccessApp(ctx context.Context, name, hostname, policyID string) error {
	want := accessApp{Name: name, Domain: hostname, Type: "self_hosted", SessionDuration: "24h", Policies: []accessAppPolicy{{ID: policyID, Precedence: 1}}}
	apps, err := c.accessApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Name == name {
			if err := c.do(ctx, http.MethodPut, c.account("/access/apps/"+a.ID), want, nil); err != nil {
				return fmt.Errorf("updating Access application %s: %w", name, err)
			}
			return nil
		}
	}
	if err := c.do(ctx, http.MethodPost, c.account("/access/apps"), want, nil); err != nil {
		return fmt.Errorf("creating Access application %s: %w", name, err)
	}
	return nil
}

// DeleteAccessApp deletes the Access application named name, if any.
func (c *Client) DeleteAccessApp(ctx context.Context, name string) error {
	apps, err := c.accessApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Name == name {
			if err := c.do(ctx, http.MethodDelete, c.account("/access/apps/"+a.ID), nil, nil); err != nil {
				return fmt.Errorf("deleting Access application %s: %w", name, err)
			}
		}
	}
	return nil
}

// HasOneTimePIN reports whether the account's Zero Trust organization
// lets users log in with a one-time PIN sent by e-mail - the login the
// Access applications rely on.
func (c *Client) HasOneTimePIN(ctx context.Context) (bool, error) {
	var idps []struct {
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, c.account("/access/identity_providers"), nil, &idps); err != nil {
		return false, fmt.Errorf("listing Access login methods: %w", err)
	}
	for _, idp := range idps {
		if idp.Type == "onetimepin" {
			return true, nil
		}
	}
	return false, nil
}

// EnableOneTimePIN adds the one-time PIN login method.
func (c *Client) EnableOneTimePIN(ctx context.Context) error {
	body := map[string]any{"name": "One-time PIN", "type": "onetimepin", "config": map[string]any{}}
	if err := c.do(ctx, http.MethodPost, c.account("/access/identity_providers"), body, nil); err != nil {
		return fmt.Errorf("enabling the one-time PIN login: %w", err)
	}
	return nil
}
