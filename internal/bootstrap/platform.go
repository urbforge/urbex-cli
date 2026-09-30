package bootstrap

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/gitea"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/secrets"
	platformassets "github.com/urbforge/urbex-cli/platform"
)

// Fixed names of what bootstrap sets up inside Gitea and Komodo.
const (
	GiteaAdminUser   = "urbex-admin"
	KomodoAdminUser  = "urbex-admin"
	KomodoServerName = "urbex-komodo" // Periphery server + builder on the Komodo LXC
	KomodoVersion    = "2.2.0"        // Core and Periphery must match exactly
	GitOpsRepoName   = "gitops"

	// TrunkBranch is the one long-lived branch of every repo urbex
	// manages: projects are developed trunk-based, and the GitOps repo is
	// deployed from it.
	TrunkBranch = "main"
	// ReleaseAction is the Komodo Action that builds a project's release
	// tags; GitOpsProcedure is the Komodo Procedure that redeploys the
	// Stacks whose folder of the GitOps repo changed. See
	// docs/decisions/0020-trunk-releases-gitops-environments.md.
	ReleaseAction   = "urbex-release"
	GitOpsProcedure = "urbex-gitops"
)

// Scopes of the Gitea token bootstrap creates, used by the CLI and by
// Komodo, which clones repos and pushes/pulls images with it.
var cliTokenScopes = []string{"write:organization", "write:repository", "write:package"}

// Endpoints are the LAN addresses of the base services, derived from the
// deterministic static-IP allocation in urbex.platform.yaml. They are
// plain HTTP until ingress/TLS exists - see
// docs/decisions/0018-image-build-komodo-gitea-registry.md.
type Endpoints struct {
	GiteaURL  string // e.g. http://192.168.1.200:3000
	Registry  string // e.g. 192.168.1.200:3000 (Gitea's container registry)
	KomodoURL string // e.g. http://192.168.1.201:9120
}

// EndpointsFor computes the base services' endpoints for cfg.
func EndpointsFor(cfg *platform.Config) (Endpoints, error) {
	ips, _, err := allocateIPs(cfg)
	if err != nil {
		return Endpoints{}, err
	}
	giteaIP := ips[Hostname("gitea")]
	return Endpoints{
		GiteaURL:  "http://" + giteaIP + ":3000",
		Registry:  giteaIP + ":3000",
		KomodoURL: "http://" + ips[Hostname("komodo")] + ":9120",
	}, nil
}

// ActionWebhook is the URL Gitea calls to run a Komodo Action on a push
// to any ref - branches and tags alike.
func (e Endpoints) ActionWebhook(action string) string {
	return e.KomodoURL + "/listener/github/action/" + action + "/__ANY__"
}

// ProcedureWebhook is the URL Gitea calls to run a Komodo Procedure on a
// push to branch.
func (e Endpoints) ProcedureWebhook(procedure, branch string) string {
	return e.KomodoURL + "/listener/github/procedure/" + procedure + "/" + branch
}

// RepoURL is the http clone URL of org/repo on Gitea.
func (e Endpoints) RepoURL(org, repo string) string {
	return e.GiteaURL + "/" + url.PathEscape(org) + "/" + url.PathEscape(repo) + ".git"
}

// GiteaAPI is the subset of the Gitea client bootstrap uses.
type GiteaAPI interface {
	WaitReady(ctx context.Context, timeout time.Duration) error
	CreateToken(ctx context.Context, username, password, name string, scopes []string) (string, error)
	EnsureOrg(ctx context.Context, org string) error
	EnsureRepo(ctx context.Context, owner, name string) error
	EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, branchFilter string) error
}

// KomodoAPI is the subset of the Komodo client bootstrap uses.
type KomodoAPI interface {
	WaitReady(ctx context.Context, timeout time.Duration) error
	Login(ctx context.Context, username, password string) (string, error)
	CreateAPIKey(ctx context.Context, jwt, name string) (string, string, error)
	CreateOnboardingKey(ctx context.Context, name string) (string, error)
	EnsureGitProviderAccount(ctx context.Context, domain string, https bool, username, token string) error
	EnsureDockerRegistryAccount(ctx context.Context, domain, username, token string) error
	EnsureAction(ctx context.Context, name string, cfg komodo.ActionConfig) error
	EnsureProcedure(ctx context.Context, name string, cfg komodo.ProcedureConfig) error
}

// Clients builds API clients and syncs repos; replaced by fakes in tests.
type Clients struct {
	Gitea    func(baseURL, token string) GiteaAPI
	Komodo   func(baseURL, apiKey, apiSecret string) KomodoAPI
	SyncRepo func(ctx context.Context, dir, remoteURL, token, message string) error
}

// DefaultClients returns Clients backed by the real HTTP clients and git.
func DefaultClients() Clients {
	return Clients{
		Gitea:    func(u, t string) GiteaAPI { return gitea.New(u, t) },
		Komodo:   func(u, k, s string) KomodoAPI { return komodo.New(u, k, s) },
		SyncRepo: SyncRepo,
	}
}

// ansibleSecretEnv are the secrets the base-service roles read with
// lookup('env', ...). ensureSecrets generates any that are missing.
var ansibleSecretEnv = []string{
	"URBEX_GITEA_ADMIN_PASSWORD",
	"URBEX_KOMODO_ADMIN_PASSWORD",
	"URBEX_KOMODO_DB_PASSWORD",
	"URBEX_KOMODO_JWT_SECRET",
	"URBEX_KOMODO_WEBHOOK_SECRET",
	"URBEX_KEYCLOAK_ADMIN_PASSWORD",
	"URBEX_GRAFANA_ADMIN_PASSWORD",
}

// ensureSecrets generates any missing platform secret the base-service
// Ansible roles need, persisting them to ~/.urbex/credentials.yaml.
func ensureSecrets(creds *credentials.Credentials) error {
	generated := map[string]string{}
	for _, name := range ansibleSecretEnv {
		if creds.Get(name) != "" {
			continue
		}
		secret, err := credentials.RandomSecret()
		if err != nil {
			return err
		}
		generated[name] = secret
	}
	if len(generated) == 0 {
		return nil
	}
	return creds.Update(func(c *credentials.Credentials) {
		for name, value := range generated {
			c.Set(name, value)
		}
	})
}

// setupPlatform wires the freshly provisioned base services together:
// a Gitea token, org and GitOps repo; a Komodo API key and the onboarding
// key project LXCs join with; Komodo's git provider and registry accounts
// pointing at Gitea; the release Action and the GitOps Procedure; the
// first push of the GitOps repo, and the webhook that makes every later
// push deploy.
func (o *Orchestrator) setupPlatform(ctx context.Context, clients Clients, gitopsRepoPath string) error {
	ep, err := EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	creds := o.Credentials

	g := clients.Gitea(ep.GiteaURL, "")
	if err := g.WaitReady(ctx, 3*time.Minute); err != nil {
		return err
	}
	if creds.GiteaToken == "" {
		token, err := g.CreateToken(ctx, GiteaAdminUser, creds.GiteaAdminPassword, "urbex-cli", cliTokenScopes)
		if err != nil {
			return err
		}
		if err := creds.Update(func(c *credentials.Credentials) { c.GiteaToken = token }); err != nil {
			return fmt.Errorf("saving gitea token: %w", err)
		}
	}
	g = clients.Gitea(ep.GiteaURL, creds.GiteaToken)
	if err := g.EnsureOrg(ctx, o.Config.Git.Org); err != nil {
		return err
	}
	if err := g.EnsureRepo(ctx, o.Config.Git.Org, GitOpsRepoName); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Gitea ready at %s (org %s).\n", ep.GiteaURL, o.Config.Git.Org)

	k := clients.Komodo(ep.KomodoURL, "", "")
	if err := k.WaitReady(ctx, 3*time.Minute); err != nil {
		return err
	}
	if creds.KomodoAPIKey == "" || creds.KomodoAPISecret == "" {
		jwt, err := k.Login(ctx, KomodoAdminUser, creds.KomodoAdminPassword)
		if err != nil {
			return err
		}
		key, secret, err := k.CreateAPIKey(ctx, jwt, "urbex-cli")
		if err != nil {
			return err
		}
		if err := creds.Update(func(c *credentials.Credentials) { c.KomodoAPIKey, c.KomodoAPISecret = key, secret }); err != nil {
			return fmt.Errorf("saving komodo api key: %w", err)
		}
	}
	k = clients.Komodo(ep.KomodoURL, creds.KomodoAPIKey, creds.KomodoAPISecret)
	if creds.KomodoOnboardingKey == "" {
		// Project LXCs' Periphery agents register themselves with this key.
		key, err := k.CreateOnboardingKey(ctx, "urbex-projects")
		if err != nil {
			return err
		}
		if err := creds.Update(func(c *credentials.Credentials) { c.KomodoOnboardingKey = key }); err != nil {
			return fmt.Errorf("saving komodo onboarding key: %w", err)
		}
	}
	if err := k.EnsureGitProviderAccount(ctx, ep.Registry, false, GiteaAdminUser, creds.GiteaToken); err != nil {
		return err
	}
	if err := k.EnsureDockerRegistryAccount(ctx, ep.Registry, GiteaAdminUser, creds.GiteaToken); err != nil {
		return err
	}

	// The two pieces of Komodo that every project shares: the Action that
	// builds release tags, and the Procedure that redeploys the Stacks
	// whose folder of the GitOps repo changed.
	script, err := platformassets.Komodo.ReadFile("komodo/release.ts")
	if err != nil {
		return err
	}
	if err := k.EnsureAction(ctx, ReleaseAction, komodo.ActionConfig{FileContents: string(script), WebhookEnabled: true}); err != nil {
		return err
	}
	// On every push to the GitOps repo (its webhook), and every 15
	// minutes regardless: the periodic run converges anything a webhook
	// missed.
	gitops := komodo.ProcedureConfig{
		WebhookEnabled:  true,
		ScheduleFormat:  "Cron",
		Schedule:        "0 */15 * * * *",
		ScheduleEnabled: true,
		Stages: []komodo.Stage{
			komodo.NewStage("Deploy changed stacks", "BatchDeployStackIfChanged", "pattern", []string{"*"}),
		},
	}
	if err := k.EnsureProcedure(ctx, GitOpsProcedure, gitops); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Komodo ready at %s (builder %s).\n", ep.KomodoURL, KomodoServerName)

	// Secrets in the GitOps repo are encrypted for the platform's age key.
	recipient, err := secrets.Recipient(creds.AgeKey)
	if err != nil {
		return err
	}
	if err := secrets.WriteSopsConfig(gitopsRepoPath, recipient); err != nil {
		return fmt.Errorf("writing .sops.yaml: %w", err)
	}

	if err := clients.SyncRepo(ctx, gitopsRepoPath, ep.RepoURL(o.Config.Git.Org, GitOpsRepoName), creds.GiteaToken, "urbex bootstrap"); err != nil {
		return fmt.Errorf("syncing gitops repo: %w", err)
	}
	fmt.Fprintf(o.Out, "GitOps repo pushed to %s.\n", ep.RepoURL(o.Config.Git.Org, GitOpsRepoName))

	// From now on a push to the GitOps repo is what deploys.
	hook := ep.ProcedureWebhook(GitOpsProcedure, TrunkBranch)
	if err := g.EnsureWebhook(ctx, o.Config.Git.Org, GitOpsRepoName, hook, creds.KomodoWebhookSecret, TrunkBranch); err != nil {
		return err
	}
	return nil
}

// SyncRepo commits every change in the repo working directory at dir
// (initializing it if needed) and pushes it to the main branch of
// remoteURL. Used to keep the GitOps repo on Gitea the source of truth.
func SyncRepo(ctx context.Context, dir, remoteURL, token, message string) error {
	if _, err := gitsync.CommitAll(ctx, dir, message); err != nil {
		return err
	}
	return gitsync.Push(ctx, dir, remoteURL, token, "HEAD:refs/heads/main")
}
