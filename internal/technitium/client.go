// Package technitium is a minimal client for the parts of Technitium DNS's
// HTTP API Urbex uses: minting an API token, creating a zone, and
// setting/reading/removing A records in it. See
// docs/decisions/0026-technitium-internal-dns-records.md in urbforge/urbex.
//
// Every call is a plain GET with query parameters, including the ones
// named "delete" in Technitium's own docs - its API doesn't use the HTTP
// method to mean anything, verified against a real server.
package technitium

import (
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

// Client talks to one Technitium DNS server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a Client for baseURL, authenticating with an API token
// (see CreateToken to mint one from a username/password - construct a
// Client with an empty token to call it).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is an error Technitium reported.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return "technitium: " + e.Message }

// AlreadyExists reports whether err is Technitium refusing to create a
// zone that already exists - fine to ignore.
func AlreadyExists(err error) bool { return matches(err, "already exists") }

// NotFound reports whether err is Technitium saying the record being
// deleted doesn't exist - fine to ignore.
func NotFound(err error) bool { return matches(err, "no such record", "zone does not exist") }

func matches(err error, substrings ...string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, s := range substrings {
		if strings.Contains(strings.ToLower(apiErr.Message), s) {
			return true
		}
	}
	return false
}

// CreateToken mints a non-expiring API token for user/pass, named name -
// the one call a Client with no token yet can make.
func (c *Client) CreateToken(ctx context.Context, user, pass, name string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	q := url.Values{"user": {user}, "pass": {pass}, "tokenName": {name}}
	if err := c.do(ctx, "/api/user/createToken", q, &out); err != nil {
		return "", fmt.Errorf("creating api token: %w", err)
	}
	return out.Token, nil
}

// EnsureZone creates a Primary zone, tolerating one that already exists.
func (c *Client) EnsureZone(ctx context.Context, zone string) error {
	q := url.Values{"zone": {zone}, "type": {"Primary"}}
	if err := c.do(ctx, "/api/zones/create", q, nil); err != nil && !AlreadyExists(err) {
		return fmt.Errorf("creating zone %s: %w", zone, err)
	}
	return nil
}

// SetRecord creates or overwrites domain's A record in zone, pointing it
// at ip.
func (c *Client) SetRecord(ctx context.Context, zone, domain, ip string) error {
	q := url.Values{"zone": {zone}, "domain": {domain}, "type": {"A"}, "ipAddress": {ip}, "ttl": {"3600"}, "overwrite": {"true"}}
	if err := c.do(ctx, "/api/zones/records/add", q, nil); err != nil {
		return fmt.Errorf("setting record %s: %w", domain, err)
	}
	return nil
}

// GetRecords returns the IPs of domain's A records in zone, none if it
// has no record.
func (c *Client) GetRecords(ctx context.Context, zone, domain string) ([]string, error) {
	var out struct {
		Records []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			RData struct {
				IPAddress string `json:"ipAddress"`
			} `json:"rData"`
		} `json:"records"`
	}
	q := url.Values{"zone": {zone}, "domain": {domain}}
	if err := c.do(ctx, "/api/zones/records/get", q, &out); err != nil {
		return nil, fmt.Errorf("getting record %s: %w", domain, err)
	}
	var ips []string
	for _, r := range out.Records {
		if r.Type == "A" && strings.EqualFold(r.Name, domain) {
			ips = append(ips, r.RData.IPAddress)
		}
	}
	return ips, nil
}

// DeleteRecord removes domain's A record in zone pointing at ip,
// tolerating one that doesn't exist.
func (c *Client) DeleteRecord(ctx context.Context, zone, domain, ip string) error {
	q := url.Values{"zone": {zone}, "domain": {domain}, "type": {"A"}, "ipAddress": {ip}}
	if err := c.do(ctx, "/api/zones/records/delete", q, nil); err != nil && !NotFound(err) {
		return fmt.Errorf("deleting record %s: %w", domain, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling technitium api: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		Status       string          `json:"status"`
		ErrorMessage string          `json:"errorMessage"`
		Response     json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("technitium %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	if envelope.Status == "error" {
		return &APIError{Status: resp.StatusCode, Message: envelope.ErrorMessage}
	}
	if out != nil {
		// createToken's fields are top-level, not nested under "response".
		src := envelope.Response
		if len(src) == 0 {
			src = data
		}
		if err := json.Unmarshal(src, out); err != nil {
			return fmt.Errorf("decoding technitium %s: %w", path, err)
		}
	}
	return nil
}
