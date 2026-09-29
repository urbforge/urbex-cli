// Package gitea is a minimal Gitea API client covering what urbex needs
// from the Git server it provisions: API tokens, the org that owns every
// urbex-managed repo, the repos themselves (GitOps repo + app repos), and
// lookups in Gitea's container registry. See
// docs/decisions/0004-gitops-gitea-komodo.md and
// docs/decisions/0018-image-build-komodo-gitea-registry.md in
// urbforge/urbex.
package gitea

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

// ErrNotFound is returned (wrapped) when Gitea answers 404.
var ErrNotFound = errors.New("not found")

// Client talks to a Gitea instance's REST API (baseURL without the
// /api/v1 suffix, e.g. "http://10.0.0.200:3000").
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New creates a Client authenticated with an API token. token may be
// empty for calls that authenticate otherwise (see CreateToken).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the Gitea URL this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// WaitReady polls the version endpoint until Gitea answers or ctx is
// done - Gitea takes a few seconds to come up after `docker compose up`.
func (c *Client) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := c.do(ctx, http.MethodGet, "/version", nil, nil, nil)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gitea at %s not ready: %w", c.baseURL, err)
		case <-time.After(2 * time.Second):
		}
	}
}

// CreateToken creates a new API token for username, authenticating with
// its password (Gitea only allows creating tokens with basic auth). A
// token with the same name is deleted first, since Gitea never shows a
// token's secret again after creation - re-running bootstrap therefore
// rotates it.
func (c *Client) CreateToken(ctx context.Context, username, password, name string, scopes []string) (string, error) {
	basic := &basicAuth{username, password}
	path := "/users/" + url.PathEscape(username) + "/tokens"
	err := c.do(ctx, http.MethodDelete, path+"/"+url.PathEscape(name), nil, nil, basic)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("deleting existing token %q: %w", name, err)
	}
	var created struct {
		SHA1 string `json:"sha1"`
	}
	body := map[string]any{"name": name, "scopes": scopes}
	if err := c.do(ctx, http.MethodPost, path, body, &created, basic); err != nil {
		return "", fmt.Errorf("creating token %q: %w", name, err)
	}
	return created.SHA1, nil
}

// EnsureOrg creates org as a private organization if it doesn't exist.
func (c *Client) EnsureOrg(ctx context.Context, org string) error {
	err := c.do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org), nil, nil, nil)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("looking up org %q: %w", org, err)
	}
	body := map[string]any{"username": org, "visibility": "private"}
	if err := c.do(ctx, http.MethodPost, "/orgs", body, nil, nil); err != nil {
		return fmt.Errorf("creating org %q: %w", org, err)
	}
	return nil
}

// EnsureRepo creates owner/name as an empty private repo (default branch
// main) if it doesn't exist. owner must be an org.
func (c *Client) EnsureRepo(ctx context.Context, owner, name string) error {
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, nil, nil)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("looking up repo %s/%s: %w", owner, name, err)
	}
	body := map[string]any{"name": name, "private": true, "default_branch": "main"}
	if err := c.do(ctx, http.MethodPost, "/orgs/"+url.PathEscape(owner)+"/repos", body, nil, nil); err != nil {
		return fmt.Errorf("creating repo %s/%s: %w", owner, name, err)
	}
	return nil
}

// ContainerImageExists reports whether owner/name:tag exists in Gitea's
// container registry.
func (c *Client) ContainerImageExists(ctx context.Context, owner, name, tag string) (bool, error) {
	path := "/packages/" + url.PathEscape(owner) + "/container/" + url.PathEscape(name) + "/" + url.PathEscape(tag)
	err := c.do(ctx, http.MethodGet, path, nil, nil, nil)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("looking up image %s/%s:%s: %w", owner, name, tag, err)
}

type basicAuth struct{ username, password string }

func (c *Client) do(ctx context.Context, method, path string, body, out any, basic *basicAuth) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case basic != nil:
		req.SetBasicAuth(basic.username, basic.password)
	case c.token != "":
		req.Header.Set("Authorization", "token "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling gitea api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitea api %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding gitea api response: %w", err)
		}
	}
	return nil
}
