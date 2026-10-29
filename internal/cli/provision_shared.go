package cli

import (
	"context"
	"fmt"
	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/keycloak"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/gitea"
	"github.com/urbforge/urbex-cli/internal/imageref"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"github.com/urbforge/urbex-cli/internal/runner"
	"github.com/urbforge/urbex-cli/internal/technitium"
)

// resolveGitOpsRepoPath returns flagValue if set, else URBEX_GITOPS_REPO,
// else an error - the same convention used by `urbex bootstrap`.
func resolveGitOpsRepoPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("URBEX_GITOPS_REPO"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("--gitops-repo (or URBEX_GITOPS_REPO) is required")
}

// project is everything a command acting on one project needs: the
// orchestrator, and the rollout driver behind it for what only the CLI
// does with it (releases, status).
type project struct {
	*provision.Orchestrator
	rollout *rollout.Rollout
	creds   *credentials.Credentials
}

// newProvisionOrchestrator loads and validates everything a project
// command needs: the local app manifest and the platform config from an
// already-bootstrapped GitOps repo, plus credentials when requireCreds is
// true (plan/apply/destroy need the Proxmox token for Terraform; the
// others only talk to Gitea and Komodo). It writes validation errors to
// out itself (mirroring `urbex init`'s behavior) before returning a plain
// error.
//
// repoDir is the app repo's working copy, used by apply only to create
// the project's main branch on Gitea when it doesn't exist yet; the
// other commands pass "".
func newProvisionOrchestrator(cmd *cobra.Command, manifestPath, gitopsRepoPath, repoDir string, requireCreds bool) (*project, error) {
	out := cmd.OutOrStdout()

	m, result, err := manifest.LoadTyped(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", manifestPath, err)
	}
	if m == nil {
		fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return nil, fmt.Errorf("%s failed validation", manifestPath)
	}

	configPath := filepath.Join(gitopsRepoPath, "urbex.platform.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("%s not found - run 'urbex bootstrap --gitops-repo %s' first", configPath, gitopsRepoPath)
	}
	cfg, err := platform.LoadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", configPath, err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		fmt.Fprintf(out, "%s is invalid (%d error(s)):\n", configPath, len(errs))
		for _, e := range errs {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return nil, fmt.Errorf("%s failed validation", configPath)
	}

	creds, err := credentials.Load()
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}
	if requireCreds {
		if missing := creds.MissingForProvisioning(); len(missing) > 0 {
			fmt.Fprintln(out, "Missing required credentials:")
			for _, mk := range missing {
				fmt.Fprintf(out, "  - %s\n", mk)
			}
			return nil, fmt.Errorf("missing required credentials")
		}
	}

	ep, err := bootstrap.EndpointsFor(cfg)
	if err != nil {
		return nil, err
	}
	// The addresses come from the config: check them against the
	// services' internal names before using them (ADR-0027).
	warning, err := bootstrap.CheckAddresses(cmd.Context(), cfg, bootstrap.TechnitiumLookup(ep.TechnitiumIP))
	if err != nil {
		return nil, err
	}
	if warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+warning+".")
	}
	pve := proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure)
	o := &provision.Orchestrator{
		Manifest:    m,
		Config:      cfg,
		Credentials: creds,
		Runner:      runner.ExecRunner{Stdout: out, Stderr: cmd.ErrOrStderr()},
		Proxmox:     pve,
		Volumes:     pve,
		Out:         out,
		Images:      &imageref.Resolver{HTTP: &http.Client{Timeout: 30 * time.Second}},
		SyncGitOps: func(ctx context.Context, path, message string) error {
			return bootstrap.SyncRepo(ctx, path, ep.RepoURL(cfg.Git.Org, bootstrap.GitOpsRepoName), creds.GiteaToken, message)
		},
		RefreshGitOps: func(ctx context.Context, path string) error {
			return bootstrap.PullRepo(ctx, path, ep.RepoURL(cfg.Git.Org, bootstrap.GitOpsRepoName), creds.GiteaToken)
		},
	}
	ro := newRollout(cfg, creds, ep, repoDir, out)
	o.Rollout = ro
	o.KeycloakURL = ep.KeycloakURL
	if internal, err := bootstrap.InternalEndpointsFor(cfg); err == nil {
		o.KeycloakInternalURL = internal.KeycloakURL
	}
	if creds.KeycloakAdminPassword != "" {
		o.Keycloak = keycloak.New(ep.KeycloakURL, "admin", creds.KeycloakAdminPassword)
	}
	if cfg.Cloudflare.Enabled() && creds.CloudflareToken != "" {
		o.Cloudflare = cloudflare.New(cloudflare.DefaultBaseURL, creds.CloudflareToken, cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID)
	}
	if cfg.Cloudflare.QuickTunnel {
		o.QuickTunnel = provision.QuickTunnelURL
	}
	if creds.TechnitiumToken != "" {
		o.Technitium = technitium.New(ep.TechnitiumURL, creds.TechnitiumToken)
	}
	return &project{Orchestrator: o, rollout: ro, creds: creds}, nil
}

// newRollout builds the Gitea+Komodo rollout driver for an already
// bootstrapped platform.
func newRollout(cfg *platform.Config, creds *credentials.Credentials, ep bootstrap.Endpoints, repoDir string, out io.Writer) *rollout.Rollout {
	internal, _ := bootstrap.InternalEndpointsFor(cfg) // fails only where ep did
	return &rollout.Rollout{
		Gitea:         gitea.New(ep.GiteaURL, creds.GiteaToken),
		Komodo:        komodo.New(ep.KomodoURL, creds.KomodoAPIKey, creds.KomodoAPISecret),
		Git:           rollout.GitCLI{},
		Endpoints:     ep,
		Internal:      internal,
		Org:           cfg.Git.Org,
		Token:         creds.GiteaToken,
		WebhookSecret: creds.KomodoWebhookSecret,
		RepoDir:       repoDir,
		Out:           out,
	}
}
