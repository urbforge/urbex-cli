// Package komodo is a minimal Komodo Core (v2) API client covering what
// urbex needs to have Komodo build and roll out project services:
// authentication (local login + API keys), the git provider and docker
// registry accounts pointing at Gitea, onboarding keys and Servers
// (Periphery agents), and the Build, Deployment, and Procedure resources
// of each project environment. See
// docs/decisions/0018-image-build-komodo-gitea-registry.md and
// docs/decisions/0019-branch-environments-komodo-rollouts.md in
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
	"regexp"
	"strings"
	"time"
)

// Client talks to Komodo Core.
type Client struct {
	baseURL   string
	apiKey    string
	apiSecret string
	http      *http.Client
	// pollInterval is the pause between polls while waiting on Komodo.
	pollInterval time.Duration
}

// New creates a Client authenticated with an API key/secret pair. Both
// may be empty for Login/CreateAPIKey, which authenticate otherwise.
func New(baseURL, apiKey, apiSecret string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		apiSecret:    apiSecret,
		http:         &http.Client{Timeout: 60 * time.Second},
		pollInterval: 2 * time.Second,
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
	ImageName          string          `json:"image_name"`
	ImageTag           string          `json:"image_tag"`
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
	return c.ensureResource(ctx, "Build", name, cfg)
}

// DeploymentConfig is the subset of Komodo's DeploymentConfig urbex sets:
// one container, on one Server, running the image of a Komodo Build.
type DeploymentConfig struct {
	ServerID        string          `json:"server_id"`
	Image           DeploymentImage `json:"image"`
	RedeployOnBuild bool            `json:"redeploy_on_build"`
	Network         string          `json:"network"`
	Restart         string          `json:"restart"`
	// Ports and Environment are newline-separated "host:container" and
	// "KEY=value" entries.
	Ports       string `json:"ports"`
	Environment string `json:"environment"`
	// ExtraArgs are interpolated verbatim into `docker run`.
	ExtraArgs []string `json:"extra_args"`
}

// DeploymentImage points a Deployment at a Build's image.
type DeploymentImage struct {
	Type   string                `json:"type"`
	Params DeploymentImageParams `json:"params"`
}

type DeploymentImageParams struct {
	BuildID string `json:"build_id"`
}

// BuildImage is the DeploymentImage for the latest version of a Build.
func BuildImage(build string) DeploymentImage {
	return DeploymentImage{Type: "Build", Params: DeploymentImageParams{BuildID: build}}
}

// EnsureDeployment creates the named Deployment with cfg, or updates it
// in place.
func (c *Client) EnsureDeployment(ctx context.Context, name string, cfg DeploymentConfig) error {
	return c.ensureResource(ctx, "Deployment", name, cfg)
}

// ProcedureConfig is the subset of Komodo's ProcedureConfig urbex sets.
type ProcedureConfig struct {
	WebhookEnabled bool    `json:"webhook_enabled"`
	Stages         []Stage `json:"stages"`
}

// Stage is a set of executions run in parallel; stages run in order.
type Stage struct {
	Name       string             `json:"name"`
	Enabled    bool               `json:"enabled"`
	Executions []EnabledExecution `json:"executions"`
}

type EnabledExecution struct {
	Enabled   bool      `json:"enabled"`
	Execution Execution `json:"execution"`
}

// Execution is one Komodo operation, e.g. {RunBuild {build: name}}.
type Execution struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// NewStage builds a Stage running execType once per target, where param
// names the execution's parameter (e.g. "RunBuild", "build").
func NewStage(name, execType, param string, targets []string) Stage {
	st := Stage{Name: name, Enabled: true}
	for _, t := range targets {
		st.Executions = append(st.Executions, EnabledExecution{
			Enabled:   true,
			Execution: Execution{Type: execType, Params: map[string]string{param: t}},
		})
	}
	return st
}

// EnsureProcedure creates the named Procedure with cfg, or updates it in
// place.
func (c *Client) EnsureProcedure(ctx context.Context, name string, cfg ProcedureConfig) error {
	return c.ensureResource(ctx, "Procedure", name, cfg)
}

// ensureResource creates or updates a named resource of the given kind
// (Build, Deployment, Procedure). Komodo resolves resources by name as
// well as by id, so urbex never stores ids.
func (c *Client) ensureResource(ctx context.Context, kind, name string, cfg any) error {
	exists, err := c.resourceExists(ctx, kind, name)
	if err != nil {
		return err
	}
	if exists {
		if err := c.call(ctx, "write", "Update"+kind, map[string]any{"id": name, "config": cfg}, nil); err != nil {
			return fmt.Errorf("updating komodo %s %s: %w", strings.ToLower(kind), name, err)
		}
		return nil
	}
	if err := c.call(ctx, "write", "Create"+kind, map[string]any{"name": name, "config": cfg}, nil); err != nil {
		return fmt.Errorf("creating komodo %s %s: %w", strings.ToLower(kind), name, err)
	}
	return nil
}

func (c *Client) resourceExists(ctx context.Context, kind, name string) (bool, error) {
	var items []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "read", "List"+kind+"s", map[string]any{}, &items); err != nil {
		return false, fmt.Errorf("listing komodo %ss: %w", strings.ToLower(kind), err)
	}
	for _, it := range items {
		if it.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// DeleteResource deletes the named Build, Deployment, Procedure, or
// Server if it exists.
func (c *Client) DeleteResource(ctx context.Context, kind, name string) error {
	exists, err := c.resourceExists(ctx, kind, name)
	if err != nil || !exists {
		return err
	}
	if err := c.call(ctx, "write", "Delete"+kind, map[string]string{"id": name}, nil); err != nil {
		return fmt.Errorf("deleting komodo %s %s: %w", strings.ToLower(kind), name, err)
	}
	return nil
}

// CreateOnboardingKey creates a non-expiring onboarding key, which
// Periphery agents use to register themselves as Servers, and returns
// its private key (shown only once).
func (c *Client) CreateOnboardingKey(ctx context.Context, name string) (string, error) {
	var resp struct {
		PrivateKey string `json:"private_key"`
	}
	if err := c.call(ctx, "write", "CreateOnboardingKey", map[string]any{"name": name}, &resp); err != nil {
		return "", fmt.Errorf("creating komodo onboarding key: %w", err)
	}
	return resp.PrivateKey, nil
}

// WaitServers polls until every named Server is connected ("Ok"), i.e.
// its Periphery agent has onboarded, or timeout elapses.
//
// A Server that already exists under a name but with another key - its
// LXC was recreated, so Periphery generated a new identity - can't
// onboard again by itself: Komodo only records the key it attempted
// with. Since only a holder of the onboarding key gets that far,
// WaitServers accepts the attempted key, and returns the names of the
// Servers it re-keyed.
func (c *Client) WaitServers(ctx context.Context, names []string, timeout time.Duration) (rekeyed []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	done := map[string]bool{}
	for {
		var servers []struct {
			Name string `json:"name"`
			Info struct {
				State              string `json:"state"`
				AttemptedPublicKey string `json:"attempted_public_key"`
			} `json:"info"`
		}
		if err := c.call(ctx, "read", "ListServers", map[string]any{}, &servers); err != nil {
			return rekeyed, fmt.Errorf("listing komodo servers: %w", err)
		}
		ok := map[string]bool{}
		for _, sv := range servers {
			ok[sv.Name] = sv.Info.State == "Ok"
			if wanted[sv.Name] && !ok[sv.Name] && sv.Info.AttemptedPublicKey != "" && !done[sv.Name] {
				params := map[string]string{"server": sv.Name, "public_key": sv.Info.AttemptedPublicKey}
				if err := c.call(ctx, "write", "UpdateServerPublicKey", params, nil); err != nil {
					return rekeyed, fmt.Errorf("accepting the new key of komodo server %s: %w", sv.Name, err)
				}
				done[sv.Name] = true
				rekeyed = append(rekeyed, sv.Name)
			}
		}
		var pending []string
		for _, n := range names {
			if !ok[n] {
				pending = append(pending, n)
			}
		}
		if len(pending) == 0 {
			return rekeyed, nil
		}
		select {
		case <-ctx.Done():
			return rekeyed, fmt.Errorf("komodo servers not connected: %v", pending)
		case <-time.After(c.pollInterval):
		}
	}
}

// DeploymentStatus is what Komodo reports about a Deployment's container.
type DeploymentStatus struct {
	Name  string
	State string // e.g. "running", "not_deployed", "exited"
	Image string
}

// ListDeployments returns the status of every Deployment, by name.
func (c *Client) ListDeployments(ctx context.Context) (map[string]DeploymentStatus, error) {
	var items []struct {
		Name string `json:"name"`
		Info struct {
			State string `json:"state"`
			Image string `json:"image"`
		} `json:"info"`
	}
	if err := c.call(ctx, "read", "ListDeployments", map[string]any{}, &items); err != nil {
		return nil, fmt.Errorf("listing komodo deployments: %w", err)
	}
	out := make(map[string]DeploymentStatus, len(items))
	for _, it := range items {
		out[it.Name] = DeploymentStatus{Name: it.Name, State: it.Info.State, Image: it.Info.Image}
	}
	return out, nil
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

var (
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
	nestedUpdateID = regexp.MustCompile(`see update '([0-9a-f]{24})'`)
)

// FailureSummary returns the stage and output of the first failed log, to
// surface why an operation failed without dumping every stage. Komodo
// formats logs for its UI, so HTML tags are stripped.
func (u *Update) FailureSummary() string {
	for _, l := range u.Logs {
		if !l.Success {
			out := strings.TrimSpace(l.Stderr)
			if out == "" {
				out = strings.TrimSpace(l.Stdout)
			}
			out = htmlTag.ReplaceAllString(out, "")
			const max = 2000
			if len(out) > max {
				out = "..." + out[len(out)-max:]
			}
			return fmt.Sprintf("stage %q failed:\n%s", l.Stage, out)
		}
	}
	return "no failed stage reported"
}

// ExplainFailure returns the most specific failure behind a failed
// Update: a Procedure's log only says which execution failed and points
// at that execution's own Update, so follow the pointer.
func (c *Client) ExplainFailure(ctx context.Context, u *Update) string {
	for depth := 0; depth < 3; depth++ {
		var nested string
		for _, l := range u.Logs {
			if l.Success {
				continue
			}
			text := htmlTag.ReplaceAllString(l.Stdout+l.Stderr, "")
			if m := nestedUpdateID.FindStringSubmatch(text); m != nil {
				nested = m[1]
			}
		}
		if nested == "" {
			break
		}
		next, err := c.GetUpdate(ctx, nested)
		if err != nil {
			break
		}
		u = next
	}
	return u.FailureSummary()
}

// RunProcedure starts the named procedure and returns the Update
// tracking it.
func (c *Client) RunProcedure(ctx context.Context, name string) (*Update, error) {
	var u Update
	if err := c.call(ctx, "execute", "RunProcedure", map[string]string{"procedure": name}, &u); err != nil {
		return nil, fmt.Errorf("running komodo procedure %s: %w", name, err)
	}
	return &u, nil
}

// GetUpdate fetches an Update by id.
func (c *Client) GetUpdate(ctx context.Context, id string) (*Update, error) {
	var u Update
	if err := c.call(ctx, "read", "GetUpdate", map[string]string{"id": id}, &u); err != nil {
		return nil, fmt.Errorf("reading komodo update %s: %w", id, err)
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
