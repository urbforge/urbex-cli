// Package komodo is a minimal Komodo Core (v2) API client covering what
// urbex needs to have Komodo build project images: authentication (local
// login + API keys), the git provider and docker registry accounts
// pointing at Gitea, Build resources, and polling a build to completion.
// See docs/decisions/0004-gitops-gitea-komodo.md and
// docs/decisions/0018-image-build-komodo-gitea-registry.md in
// urbforge/urbex.
//
// Komodo's API is RPC-style: POST {base}/{read|write|execute}/{Type} with
// the request's params as JSON body.
package komodo

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

// Client talks to Komodo Core.
type Client struct {
	baseURL   string
	apiKey    string
	apiSecret string
	http      *http.Client
}

// New creates a Client authenticated with an API key/secret pair. Both
// may be empty for Login/CreateAPIKey, which authenticate otherwise.
func New(baseURL, apiKey, apiSecret string) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 60 * time.Second},
	}
}

// WaitReady polls Komodo Core until it answers or timeout elapses.
func (c *Client) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %s", resp.Status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("komodo at %s not ready: %w", c.baseURL, err)
		case <-time.After(2 * time.Second):
		}
	}
}

// Login authenticates a local user and returns a JWT for CreateAPIKey.
func (c *Client) Login(ctx context.Context, username, password string) (string, error) {
	var resp struct {
		Type string `json:"type"`
		Data struct {
			JWT string `json:"jwt"`
		} `json:"data"`
	}
	body := map[string]string{"username": username, "password": password}
	if err := c.post(ctx, "/auth/login/LoginLocalUser", body, &resp, nil); err != nil {
		return "", fmt.Errorf("logging in to komodo as %s: %w", username, err)
	}
	if resp.Data.JWT == "" {
		return "", fmt.Errorf("logging in to komodo as %s: unexpected response type %q (is two-factor auth enabled?)", username, resp.Type)
	}
	return resp.Data.JWT, nil
}

// CreateAPIKey creates a non-expiring API key for the user owning jwt.
func (c *Client) CreateAPIKey(ctx context.Context, jwt, name string) (key, secret string, err error) {
	var resp struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}
	body := map[string]any{"name": name, "expires": 0}
	if err := c.post(ctx, "/auth/manage/CreateApiKey", body, &resp, map[string]string{"Authorization": jwt}); err != nil {
		return "", "", fmt.Errorf("creating komodo api key: %w", err)
	}
	return resp.Key, resp.Secret, nil
}

type account struct {
	ID struct {
		OID string `json:"$oid"`
	} `json:"_id"`
	Domain   string `json:"domain"`
	Username string `json:"username"`
}

// EnsureGitProviderAccount creates or updates the account Komodo uses to
// clone repos from domain (host[:port], no scheme).
func (c *Client) EnsureGitProviderAccount(ctx context.Context, domain string, https bool, username, token string) error {
	fields := map[string]any{"domain": domain, "https": https, "username": username, "token": token}
	return c.ensureAccount(ctx, "GitProviderAccount", domain, username, fields)
}

// EnsureDockerRegistryAccount creates or updates the account Komodo uses
// to push images to the registry at domain (host[:port]).
func (c *Client) EnsureDockerRegistryAccount(ctx context.Context, domain, username, token string) error {
	fields := map[string]any{"domain": domain, "username": username, "token": token}
	return c.ensureAccount(ctx, "DockerRegistryAccount", domain, username, fields)
}

func (c *Client) ensureAccount(ctx context.Context, kind, domain, username string, fields map[string]any) error {
	var existing []account
	filter := map[string]string{"domain": domain, "username": username}
	if err := c.call(ctx, "read", "List"+kind+"s", filter, &existing); err != nil {
		return fmt.Errorf("listing komodo %s: %w", kind, err)
	}
	if len(existing) > 0 {
		params := map[string]any{"id": existing[0].ID.OID, "account": fields}
		if err := c.call(ctx, "write", "Update"+kind, params, nil); err != nil {
			return fmt.Errorf("updating komodo %s %s: %w", kind, domain, err)
		}
		return nil
	}
	if err := c.call(ctx, "write", "Create"+kind, map[string]any{"account": fields}, nil); err != nil {
		return fmt.Errorf("creating komodo %s %s: %w", kind, domain, err)
	}
	return nil
}

// BuildConfig is the subset of Komodo's BuildConfig urbex sets. Fields
// map 1:1 to Komodo's JSON names.
type BuildConfig struct {
	BuilderID          string          `json:"builder_id"`
	GitProvider        string          `json:"git_provider"`
	GitHTTPS           bool            `json:"git_https"`
	GitAccount         string          `json:"git_account"`
	Repo               string          `json:"repo"`
	Branch             string          `json:"branch"`
	Commit             string          `json:"commit"`
	ImageName          string          `json:"image_name"`
	ImageRegistry      []ImageRegistry `json:"image_registry"`
	IncludeCommitTag   bool            `json:"include_commit_tag"`
	IncludeLatestTag   bool            `json:"include_latest_tag"`
	IncludeVersionTags bool            `json:"include_version_tags"`
	AutoIncrement      bool            `json:"auto_increment_version"`
	WebhookEnabled     bool            `json:"webhook_enabled"`
	BuildPath          string          `json:"build_path"`
	DockerfilePath     string          `json:"dockerfile_path"`
	PreBuild           SystemCommand   `json:"pre_build"`
	BuildArgs          string          `json:"build_args"`
}

// ImageRegistry is where a build pushes its image:
// {Domain}/{Organization}/{image_name}:{tag}.
type ImageRegistry struct {
	Domain       string `json:"domain"`
	Account      string `json:"account"`
	Organization string `json:"organization"`
}

// SystemCommand is a command Komodo runs in a directory of the cloned
// repo (used as pre_build).
type SystemCommand struct {
	Path      string `json:"path"`
	Command   string `json:"command"`
	ShellMode bool   `json:"shell_mode"`
}

// EnsureBuild creates the named Build with cfg, or updates it in place.
func (c *Client) EnsureBuild(ctx context.Context, name string, cfg BuildConfig) error {
	var builds []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "read", "ListBuilds", map[string]any{}, &builds); err != nil {
		return fmt.Errorf("listing komodo builds: %w", err)
	}
	for _, b := range builds {
		if b.Name == name {
			if err := c.call(ctx, "write", "UpdateBuild", map[string]any{"id": name, "config": cfg}, nil); err != nil {
				return fmt.Errorf("updating komodo build %s: %w", name, err)
			}
			return nil
		}
	}
	if err := c.call(ctx, "write", "CreateBuild", map[string]any{"name": name, "config": cfg}, nil); err != nil {
		return fmt.Errorf("creating komodo build %s: %w", name, err)
	}
	return nil
}

// Update is Komodo's record of one operation (e.g. a build run).
type Update struct {
	ID struct {
		OID string `json:"$oid"`
	} `json:"_id"`
	Operation  string `json:"operation"`
	Status     string `json:"status"`
	Success    bool   `json:"success"`
	CommitHash string `json:"commit_hash"`
	Logs       []Log  `json:"logs"`
}

// Log is one stage of an Update.
type Log struct {
	Stage   string `json:"stage"`
	Command string `json:"command"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Success bool   `json:"success"`
}

// FailureSummary returns the stage and stderr of the first failed log, to
// surface why a build failed without dumping every stage.
func (u *Update) FailureSummary() string {
	for _, l := range u.Logs {
		if !l.Success {
			out := strings.TrimSpace(l.Stderr)
			if out == "" {
				out = strings.TrimSpace(l.Stdout)
			}
			const max = 2000
			if len(out) > max {
				out = "..." + out[len(out)-max:]
			}
			return fmt.Sprintf("stage %q failed:\n%s", l.Stage, out)
		}
	}
	return "no failed stage reported"
}

// RunBuild starts the named build and returns the Update tracking it.
func (c *Client) RunBuild(ctx context.Context, name string) (*Update, error) {
	var u Update
	if err := c.call(ctx, "execute", "RunBuild", map[string]string{"build": name}, &u); err != nil {
		return nil, fmt.Errorf("running komodo build %s: %w", name, err)
	}
	return &u, nil
}

// WaitUpdate polls an Update until Komodo reports it Complete.
func (c *Client) WaitUpdate(ctx context.Context, id string, interval time.Duration) (*Update, error) {
	for {
		var u Update
		if err := c.call(ctx, "read", "GetUpdate", map[string]string{"id": id}, &u); err != nil {
			return nil, fmt.Errorf("polling komodo update %s: %w", id, err)
		}
		if u.Status == "Complete" {
			return &u, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (c *Client) call(ctx context.Context, kind, typ string, params, out any) error {
	headers := map[string]string{"X-Api-Key": c.apiKey, "X-Api-Secret": c.apiSecret}
	return c.post(ctx, "/"+kind+"/"+typ, params, out, headers)
}

func (c *Client) post(ctx context.Context, path string, body, out any, headers map[string]string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling komodo api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(msg, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("komodo api %s returned %s: %s", path, resp.Status, apiErr.Error)
		}
		return fmt.Errorf("komodo api %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding komodo api response: %w", err)
		}
	}
	return nil
}
