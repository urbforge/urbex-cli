// Package grafana talks to the platform's Grafana: what urbex doctor
// needs of its alerting (urbforge/urbex-cli#95).
package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is Grafana's HTTP API, as its admin.
type Client struct {
	BaseURL, User, Password string
	HTTP                    *http.Client
}

// New returns a client for Grafana at baseURL (http://host:3000).
func New(baseURL, user, password string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), User: user, Password: password,
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Integration is one channel of a contact point.
type Integration struct {
	UID  string `json:"uid"`
	Type string `json:"type"`
	raw  json.RawMessage
}

// TestResult is what a channel answered a test message: Error is
// Grafana's, with the channel's URL redacted.
type TestResult struct {
	Integration
	OK    bool
	Error string
}

const receiversPath = "/apis/notifications.alerting.grafana.app/v1beta1/namespaces/default/receivers"

// receiver is one contact point, in Grafana 13's receivers API.
type receiver struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Title        string            `json:"title"`
		Integrations []json.RawMessage `json:"integrations"`
	} `json:"spec"`
}

// TestContactPoint sends a test notification through each channel of the
// contact point named title, with the settings and secrets Grafana holds.
// It returns an error when Grafana can't be reached or has no such
// contact point.
func (c *Client) TestContactPoint(ctx context.Context, title, summary string) ([]TestResult, error) {
	var list struct {
		Items []receiver `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, receiversPath, nil, &list); err != nil {
		return nil, err
	}
	var rc *receiver
	for i := range list.Items {
		if list.Items[i].Spec.Title == title {
			rc = &list.Items[i]
		}
	}
	if rc == nil {
		return nil, fmt.Errorf("grafana has no contact point %s", title)
	}
	var results []TestResult
	for _, raw := range rc.Spec.Integrations {
		in := Integration{raw: raw}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		body := map[string]any{
			"integration": in.raw,
			"alert": map[string]any{
				"labels":      map[string]string{"alertname": "urbex doctor test", "severity": "info"},
				"annotations": map[string]string{"summary": summary},
			},
		}
		var res struct {
			Status, Error string
		}
		if err := c.do(ctx, http.MethodPost, receiversPath+"/"+rc.Metadata.Name+"/test", body, &res); err != nil {
			return nil, err
		}
		results = append(results, TestResult{Integration: in, OK: res.Status == "success", Error: res.Error})
	}
	return results, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.User, c.Password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("grafana: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("grafana: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}
