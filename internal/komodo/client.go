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

// StackConfig is the subset of Komodo's StackConfig urbex sets: a
// compose project on one Server, whose files live in a folder of a git
// repo.
type StackConfig struct {
	ServerID    string `json:"server_id"`
	GitProvider string `json:"git_provider"`
	GitHTTPS    bool   `json:"git_https"`
	GitAccount  string `json:"git_account"`
	Repo        string `json:"repo"`
	Branch      string `json:"branch"`
	// RunDirectory is the folder of the repo holding the compose file.
	RunDirectory string `json:"run_directory"`
	// AdditionalEnvFiles are passed to compose with --env-file, for
	// interpolation in the compose file.
	AdditionalEnvFiles []EnvFile `json:"additional_env_files"`
	// ConfigFiles are other files whose changes should redeploy the Stack.
	ConfigFiles      []ConfigFile `json:"config_files"`
	RegistryProvider string       `json:"registry_provider"`
	RegistryAccount  string       `json:"registry_account"`
	// ComposeCmdWrapper wraps `docker compose up`; [[COMPOSE_COMMAND]]
	// stands for the wrapped command.
	ComposeCmdWrapper string `json:"compose_cmd_wrapper"`
	WebhookEnabled    bool   `json:"webhook_enabled"`
	// Environment is written to .env in the run directory and passed to
	// compose; [[VARIABLE]]s are interpolated by Komodo.
	Environment string `json:"environment,omitempty"`
	// FilesOnHost: the compose files are on the server, in RunDirectory
	// (a path inside Periphery's container), not in a repo.
	FilesOnHost bool `json:"files_on_host,omitempty"`
	// ProjectName is the compose project; empty, the Stack's name. Set to
	// an existing project's, the Stack adopts its running containers.
	ProjectName string `json:"project_name,omitempty"`
	// FilePaths are the compose files in RunDirectory; empty, compose's
	// default names.
	FilePaths []string `json:"file_paths,omitempty"`
}

// EnvFile is a compose --env-file, tracked for changes.
type EnvFile struct {
	Path  string `json:"path"`
	Track bool   `json:"track"`
}

// ConfigFile is a file a Stack depends on. Requires says what a change
// to it calls for: "Redeploy", "Restart", or "None" (Komodo's default,
// which makes tracking the file pointless for deploys).
type ConfigFile struct {
	Path     string `json:"path"`
	Requires string `json:"requires"`
}

// EnsureStack creates the named Stack with cfg, or updates it in place.
func (c *Client) EnsureStack(ctx context.Context, name string, cfg StackConfig) error {
	return c.whenNotBusy(ctx, func() error { return c.ensureResource(ctx, "Stack", name, cfg) })
}

// BusyTimeout bounds how long a change waits for a resource Komodo reports
// busy (a deploy or build in progress, e.g. started by a push).
var BusyTimeout = 10 * time.Minute

// whenNotBusy runs change, and runs it again while Komodo refuses it
// because the resource is busy, until BusyTimeout.
func (c *Client) whenNotBusy(ctx context.Context, change func() error) error {
	deadline := time.Now().Add(BusyTimeout)
	for {
		err := change()
		if err == nil || !strings.Contains(err.Error(), "busy") || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(c.pollInterval):
		}
	}
}

// RunAction runs the named Action with args (merged over its defaults)
// and waits for it.
func (c *Client) RunAction(ctx context.Context, name string, args map[string]any) (*Update, error) {
	var u Update
	if err := c.call(ctx, "execute", "RunAction", map[string]any{"action": name, "args": args}, &u); err != nil {
		return nil, fmt.Errorf("running komodo action %s: %w", name, err)
	}
	return c.WaitUpdate(ctx, u.ID.OID, c.pollInterval)
}

// EnsureVariable creates the named Komodo variable or updates its value.
// Secret variables are hidden in Komodo's UI and logs; resources use
// either kind as [[NAME]].
func (c *Client) EnsureVariable(ctx context.Context, name, value string, secret bool) error {
	var vars []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "read", "ListVariables", map[string]any{}, &vars); err != nil {
		return fmt.Errorf("listing komodo variables: %w", err)
	}
	for _, v := range vars {
		if v.Name == name {
			if err := c.call(ctx, "write", "UpdateVariableValue", map[string]any{"name": name, "value": value}, nil); err != nil {
				return fmt.Errorf("updating komodo variable %s: %w", name, err)
			}
			return c.call(ctx, "write", "UpdateVariableIsSecret", map[string]any{"name": name, "is_secret": secret}, nil)
		}
	}
	params := map[string]any{"name": name, "value": value, "is_secret": secret, "description": "Managed by urbex bootstrap"}
	if err := c.call(ctx, "write", "CreateVariable", params, nil); err != nil {
		return fmt.Errorf("creating komodo variable %s: %w", name, err)
	}
	return nil
}

// ActionConfig is the subset of Komodo's ActionConfig urbex sets.
type ActionConfig struct {
	// FileContents is the TypeScript the Action runs.
	FileContents   string `json:"file_contents"`
	WebhookEnabled bool   `json:"webhook_enabled"`
}

// EnsureAction creates the named Action with cfg, or updates it in place.
func (c *Client) EnsureAction(ctx context.Context, name string, cfg ActionConfig) error {
	return c.ensureResource(ctx, "Action", name, cfg)
}

// ProcedureConfig is the subset of Komodo's ProcedureConfig urbex sets.
type ProcedureConfig struct {
	WebhookEnabled bool    `json:"webhook_enabled"`
	Stages         []Stage `json:"stages"`
	// Schedule is a cron expression with seconds
	// ("second minute hour day month weekday"), used when ScheduleEnabled.
	ScheduleFormat  string `json:"schedule_format"`
	Schedule        string `json:"schedule"`
	ScheduleEnabled bool   `json:"schedule_enabled"`
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

// Execution is one Komodo operation, e.g. {BatchDeployStackIfChanged
// {pattern: "*"}}.
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
// (Build, Stack, Procedure, Action). Komodo resolves resources by name as
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

// DeleteResource deletes the named Build, Stack, Procedure, Action, or
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
	// A deadline checked between polls, not a context deadline: a poll
	// cut short would hide which servers are still missing.
	deadline := time.Now().Add(timeout)
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
		if time.Now().After(deadline) {
			return rekeyed, fmt.Errorf("komodo servers not connected: %v", pending)
		}
		select {
		case <-ctx.Done():
			return rekeyed, ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

// StackStatus is what Komodo reports about a Stack.
type StackStatus struct {
	Name  string
	State string // e.g. "running", "down", "unhealthy", "deploying"
	// Images are the images of the Stack's running services.
	Images []string
}

// ListStacks returns the status of every Stack, by name.
func (c *Client) ListStacks(ctx context.Context) (map[string]StackStatus, error) {
	var items []struct {
		Name string `json:"name"`
		Info struct {
			State    string `json:"state"`
			Services []struct {
				Image string `json:"image"`
			} `json:"services"`
		} `json:"info"`
	}
	if err := c.call(ctx, "read", "ListStacks", map[string]any{}, &items); err != nil {
		return nil, fmt.Errorf("listing komodo stacks: %w", err)
	}
	out := make(map[string]StackStatus, len(items))
	for _, it := range items {
		st := StackStatus{Name: it.Name, State: it.Info.State}
		for _, svc := range it.Info.Services {
			st.Images = append(st.Images, svc.Image)
		}
		out[it.Name] = st
	}
	return out, nil
}

// DestroyStack takes the named Stack's containers down (`compose down`)
// and waits for it, if the Stack exists.
func (c *Client) DestroyStack(ctx context.Context, name string) error {
	exists, err := c.resourceExists(ctx, "Stack", name)
	if err != nil || !exists {
		return err
	}
	var u Update
	if err := c.call(ctx, "execute", "DestroyStack", map[string]string{"stack": name}, &u); err != nil {
		return fmt.Errorf("destroying komodo stack %s: %w", name, err)
	}
	if _, err := c.WaitUpdate(ctx, u.ID.OID, c.pollInterval); err != nil {
		return err
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

// RunProcedure starts the named procedure without waiting for it.
func (c *Client) RunProcedure(ctx context.Context, name string) error {
	if err := c.call(ctx, "execute", "RunProcedure", map[string]string{"procedure": name}, nil); err != nil {
		return fmt.Errorf("running komodo procedure %s: %w", name, err)
	}
	return nil
}

// DeployStack deploys the named Stack from its repo as it is now and
// waits for the result.
func (c *Client) DeployStack(ctx context.Context, name string) (*Update, error) {
	var result *Update
	// A deploy already running (a push, the reconcile schedule) makes
	// Komodo refuse this one as busy: wait for it, then deploy.
	err := c.whenNotBusy(ctx, func() error {
		var u Update
		if err := c.call(ctx, "execute", "DeployStack", map[string]string{"stack": name}, &u); err != nil {
			return fmt.Errorf("deploying komodo stack %s: %w", name, err)
		}
		done, err := c.WaitUpdate(ctx, u.ID.OID, c.pollInterval)
		result = done
		if err == nil && !done.Success && done.mentions("busy") {
			return fmt.Errorf("komodo stack %s is busy", name)
		}
		return err
	})
	return result, err
}

// mentions reports whether any of the update's logs contains text.
func (u *Update) mentions(text string) bool {
	for _, l := range u.Logs {
		if strings.Contains(l.Stdout+l.Stderr, text) {
			return true
		}
	}
	return false
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

// WaitIdle waits until none of the given Builds is building and the given
// Action (if any) has no run in progress - e.g. before deleting them, which
// Komodo refuses for a busy Build ("Build busy").
func (c *Client) WaitIdle(ctx context.Context, builds []string, action string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		busy, err := c.busy(ctx, builds, action)
		if err != nil || busy == "" {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("komodo %s still busy after %s", busy, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

// busy names the first of the Builds or the Action that is running, or
// returns "" when none is.
func (c *Client) busy(ctx context.Context, builds []string, action string) (string, error) {
	// Komodo reports a Build's state as a boolean and an Action's as the
	// number of runs in progress: anything but false or 0 is busy.
	active := func(v json.RawMessage) bool {
		s := strings.TrimSpace(string(v))
		return s != "" && s != "false" && s != "0" && s != "null"
	}
	if action != "" {
		var st struct {
			Running json.RawMessage `json:"running"`
		}
		if err := c.call(ctx, "read", "GetActionActionState", map[string]string{"action": action}, &st); err != nil {
			return "", fmt.Errorf("reading komodo action %s: %w", action, err)
		}
		if active(st.Running) {
			return "action " + action, nil
		}
	}
	for _, b := range builds {
		exists, err := c.resourceExists(ctx, "Build", b)
		if err != nil {
			return "", err
		}
		if !exists {
			continue
		}
		var st struct {
			Building json.RawMessage `json:"building"`
		}
		if err := c.call(ctx, "read", "GetBuildActionState", map[string]string{"build": b}, &st); err != nil {
			return "", fmt.Errorf("reading komodo build %s: %w", b, err)
		}
		if active(st.Building) {
			return "build " + b, nil
		}
	}
	return "", nil
}

// TagResource sets the tags of a resource (kind "Server", "Stack",
// "Build", "Action", "Procedure"), creating the tags that don't exist yet.
// The resource ends up with exactly these tags.
func (c *Client) TagResource(ctx context.Context, kind, name string, tags ...string) error {
	var existing []struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, "read", "ListTags", map[string]any{}, &existing); err != nil {
		return fmt.Errorf("listing komodo tags: %w", err)
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.Name] = true
	}
	for _, t := range tags {
		if !have[t] {
			if err := c.call(ctx, "write", "CreateTag", map[string]string{"name": t}, nil); err != nil {
				return fmt.Errorf("creating komodo tag %s: %w", t, err)
			}
			have[t] = true
		}
	}
	params := map[string]any{"target": map[string]string{"type": kind, "id": name}, "tags": tags}
	if err := c.call(ctx, "write", "UpdateResourceMeta", params, nil); err != nil {
		return fmt.Errorf("tagging komodo %s %s: %w", strings.ToLower(kind), name, err)
	}
	return nil
}
