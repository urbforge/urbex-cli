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

// BranchExists reports whether owner/repo has the given branch.
func (c *Client) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/branches/"+url.PathEscape(branch), nil, nil, nil)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("looking up branch %s of %s/%s: %w", branch, owner, repo, err)
}

type webhook struct {
	ID     int64 `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// EnsureWebhook makes owner/repo call targetURL (JSON, signed with
// secret) on every push matching branchFilter - a branch name, or "*"
// for every branch and tag - creating the webhook or updating the
// existing one with the same URL.
func (c *Client) EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, branch string) error {
	hook, err := c.findWebhook(ctx, owner, repo, targetURL)
	if err != nil {
		return err
	}
	body := map[string]any{
		"active":        true,
		"events":        []string{"push"},
		"branch_filter": branch,
		"config":        map[string]string{"url": targetURL, "content_type": "json", "secret": secret},
	}
	if hook != nil {
		path := fmt.Sprintf("%s/hooks/%d", repoPath(owner, repo), hook.ID)
		if err := c.do(ctx, http.MethodPatch, path, body, nil, nil); err != nil {
			return fmt.Errorf("updating webhook of %s/%s: %w", owner, repo, err)
		}
		return nil
	}
	body["type"] = "gitea"
	if err := c.do(ctx, http.MethodPost, repoPath(owner, repo)+"/hooks", body, nil, nil); err != nil {
		return fmt.Errorf("creating webhook of %s/%s: %w", owner, repo, err)
	}
	return nil
}

// DeleteWebhook removes owner/repo's webhook to targetURL, if any.
func (c *Client) DeleteWebhook(ctx context.Context, owner, repo, targetURL string) error {
	hook, err := c.findWebhook(ctx, owner, repo, targetURL)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if hook == nil {
		return nil
	}
	path := fmt.Sprintf("%s/hooks/%d", repoPath(owner, repo), hook.ID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return fmt.Errorf("deleting webhook of %s/%s: %w", owner, repo, err)
	}
	return nil
}

func (c *Client) findWebhook(ctx context.Context, owner, repo, targetURL string) (*webhook, error) {
	var hooks []webhook
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/hooks", nil, &hooks, nil); err != nil {
		return nil, fmt.Errorf("listing webhooks of %s/%s: %w", owner, repo, err)
	}
	for i := range hooks {
		if hooks[i].Config.URL == targetURL {
			return &hooks[i], nil
		}
	}
	return nil, nil
}

// Tag is a tag of a repo and the commit it points to.
type Tag struct {
	Name   string
	Commit string // full hash
}

// ListTags returns owner/repo's tags.
func (c *Client) ListTags(ctx context.Context, owner, repo string) ([]Tag, error) {
	var all []Tag
	for page := 1; ; page++ {
		var tags []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		path := fmt.Sprintf("%s/tags?page=%d&limit=50", repoPath(owner, repo), page)
		if err := c.do(ctx, http.MethodGet, path, nil, &tags, nil); err != nil {
			return nil, fmt.Errorf("listing tags of %s/%s: %w", owner, repo, err)
		}
		for _, t := range tags {
			all = append(all, Tag{Name: t.Name, Commit: t.Commit.SHA})
		}
		if len(tags) < 50 {
			return all, nil
		}
	}
}

// ListOrgRepos returns the full names (org/repo) of the organization's
// repositories.
func (c *Client) ListOrgRepos(ctx context.Context, org string) ([]string, error) {
	var all []string
	for page := 1; ; page++ {
		var repos []struct {
			FullName string `json:"full_name"`
		}
		path := fmt.Sprintf("/orgs/%s/repos?page=%d&limit=50", url.PathEscape(org), page)
		if err := c.do(ctx, http.MethodGet, path, nil, &repos, nil); err != nil {
			return nil, fmt.Errorf("listing repositories of %s: %w", org, err)
		}
		for _, r := range repos {
			all = append(all, r.FullName)
		}
		if len(repos) < 50 {
			return all, nil
		}
	}
}

// BranchCommit returns the full hash of the head of owner/repo's branch.
func (c *Client) BranchCommit(ctx context.Context, owner, repo, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/branches/"+url.PathEscape(branch), nil, &b, nil); err != nil {
		return "", fmt.Errorf("reading branch %s of %s/%s: %w", branch, owner, repo, err)
	}
	return b.Commit.ID, nil
}

// ResolveCommit returns the full hash of ref in owner/repo: a commit
// hash (abbreviated or not), a branch, or a tag.
func (c *Client) ResolveCommit(ctx context.Context, owner, repo, ref string) (string, error) {
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo)+"/git/commits/"+url.PathEscape(ref), nil, &commit, nil); err != nil {
		return "", fmt.Errorf("resolving %q in %s/%s: %w", ref, owner, repo, err)
	}
	return commit.SHA, nil
}

// CreateTag creates an annotated tag on owner/repo at target (a branch
// name or a full commit hash).
func (c *Client) CreateTag(ctx context.Context, owner, repo, tag, target, message string) error {
	body := map[string]string{"tag_name": tag, "target": target, "message": message}
	if err := c.do(ctx, http.MethodPost, repoPath(owner, repo)+"/tags", body, nil, nil); err != nil {
		return fmt.Errorf("creating tag %s on %s/%s: %w", tag, owner, repo, err)
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

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

type basicAuth struct{ username, password string }

// RetryDelay is how long a failed read waits before its first retry; each
// further retry waits one more RetryDelay.
var RetryDelay = time.Second

// readAttempts bounds how often a read that Gitea answered with a server
// error is tried. Gitea panics on some reads of a repo it is still
// updating - e.g. a branch, right after the first push to an empty repo -
// and answers correctly a moment later. Writes are never retried.
const readAttempts = 5

type serverError struct{ err error }

func (e serverError) Error() string { return e.err.Error() }

func (c *Client) do(ctx context.Context, method, path string, body, out any, basic *basicAuth) error {
	for attempt := 1; ; attempt++ {
		err := c.doOnce(ctx, method, path, body, out, basic)
		var se serverError
		if method != http.MethodGet || attempt == readAttempts || !errors.As(err, &se) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt) * RetryDelay):
		}
	}
}

func (c *Client) doOnce(ctx context.Context, method, path string, body, out any, basic *basicAuth) error {
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
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("gitea api %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode >= 500 {
			return serverError{err}
		}
		return err
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding gitea api response: %w", err)
		}
	}
	return nil
}
