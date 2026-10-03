package bootstrap

import (
	"context"
	"fmt"
	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/runner"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/materialize"
	"github.com/urbforge/urbex-cli/internal/tfstate"
)

// TeardownOptions controls a Teardown.
type TeardownOptions struct {
	GitOpsRepoPath string
	// Yes confirms: without it, Teardown only reports what it would
	// destroy and lose.
	Yes bool
}

// repoLister is implemented by the real Gitea client; used to say which
// repositories a teardown loses.
type repoLister interface {
	ListOrgRepos(ctx context.Context, org string) ([]string, error)
}

// Teardown destroys the platform bootstrap created: the base-service LXCs
// (Gitea, Komodo, Technitium, Keycloak, observability) and, with them,
// everything stored in them - including every repository and image on
// Gitea. It refuses while any project environment exists ('urbex destroy'
// them first). The credentials bootstrap generated are forgotten, so the
// next bootstrap starts afresh; the GitOps repo's working copy, its only
// remaining copy, is kept and committed locally.
func (o *Orchestrator) Teardown(ctx context.Context, opts TeardownOptions) error {
	if errs := o.Config.Validate(); len(errs) > 0 {
		return fmt.Errorf("invalid platform config: %v", errs)
	}
	if missing := o.Credentials.MissingForProvisioning(); len(missing) > 0 {
		return fmt.Errorf("missing required credentials: %v", missing)
	}
	if o.Config.Cloudflare.Enabled() && o.Credentials.CloudflareToken == "" {
		return fmt.Errorf("missing required credentials: [URBEX_CLOUDFLARE_TOKEN] (urbex.platform.yaml sets cloudflare.accountId)")
	}
	ep, err := EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	clients := o.Clients
	if clients.Gitea == nil {
		clients = DefaultClients()
	}
	if clients.PullRepo == nil {
		clients.PullRepo = PullRepo
	}

	// Gitea has the latest GitOps repo - for the last time.
	repoURL := ep.RepoURL(o.Config.Git.Org, GitOpsRepoName)
	if o.Credentials.GiteaToken != "" {
		if err := clients.PullRepo(ctx, opts.GitOpsRepoPath, repoURL, o.Credentials.GiteaToken); err != nil {
			fmt.Fprintf(o.Out, "Warning: could not update the GitOps repo from Gitea (%v); using the local copy.\n", err)
		}
	}

	if projects, err := remainingProjects(opts.GitOpsRepoPath); err != nil {
		return err
	} else if len(projects) > 0 {
		return fmt.Errorf("project environments still exist (%s): run 'urbex destroy <env>' for each of them first", strings.Join(projects, ", "))
	}

	tfDir := filepath.Join(opts.GitOpsRepoPath, "terraform", "base")
	var repos []string
	if g, ok := clients.Gitea(ep.GiteaURL, o.Credentials.GiteaToken).(repoLister); ok && o.Credentials.GiteaToken != "" {
		repos, _ = g.ListOrgRepos(ctx, o.Config.Git.Org) // best effort: Gitea may already be gone
	}
	fmt.Fprintln(o.Out, "This destroys the Urbex platform on Proxmox:")
	fmt.Fprintf(o.Out, "  - the base-service LXCs and their disks: %s\n", strings.Join(baseHostnames(o.Config), ", "))
	if len(repos) > 0 {
		fmt.Fprintf(o.Out, "  - everything on Gitea: %s, and their images - make sure the project repos live elsewhere too\n", strings.Join(repos, ", "))
	} else {
		fmt.Fprintln(o.Out, "  - everything on Gitea: every repository and image - make sure the project repos live elsewhere too")
	}
	fmt.Fprintln(o.Out, "  - Komodo's, Keycloak's and Grafana's configuration and data")
	if o.Config.Cloudflare.Enabled() {
		fmt.Fprintf(o.Out, "  - on Cloudflare: the tunnel %s, the platform's DNS records and Access applications\n", o.Config.Cloudflare.TunnelName)
	}
	fmt.Fprintln(o.Out, "  - the credentials bootstrap generated (passwords, tokens, keys)")
	fmt.Fprintf(o.Out, "The GitOps repo is kept, in %s: from then on its only copy.\n", opts.GitOpsRepoPath)
	if !opts.Yes {
		return fmt.Errorf("nothing destroyed: run 'urbex teardown --yes' to confirm")
	}

	if o.Config.Cloudflare.Enabled() {
		if err := o.unpublishPlatform(ctx, clients); err != nil {
			return fmt.Errorf("removing the platform from cloudflare: %w", err)
		}
	}
	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("materializing base-service assets: %w", err)
	}
	ips, prefixLen, err := allocateIPs(o.Config)
	if err != nil {
		return fmt.Errorf("allocating static IPs: %w", err)
	}
	if err := writeTFVars(tfDir, o.Config, ips, prefixLen); err != nil {
		return fmt.Errorf("writing terraform vars: %w", err)
	}
	tfEnv := []string{"TF_VAR_proxmox_api_token=" + o.Credentials.ProxmoxToken}
	if err := o.Runner.Run(ctx, Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: tfEnv}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if lister, ok := o.Proxmox.(tfstate.ContainerLister); ok {
		if err := tfstate.ForgetVanished(ctx, lister, o.Config.Proxmox.Node, o.Runner, tfDir, tfEnv, o.Out); err != nil {
			return err
		}
	}
	if err := runner.TerraformChange(ctx, o.Runner, o.Out, tfDir, "destroy", tfEnv); err != nil {
		return err
	}

	var addrs []string
	for _, ip := range ips {
		addrs = append(addrs, ip)
	}
	if err := knownhosts.Forget(opts.GitOpsRepoPath, addrs...); err != nil {
		return fmt.Errorf("updating known hosts: %w", err)
	}
	if err := o.Credentials.ClearGenerated(); err != nil {
		return fmt.Errorf("forgetting the platform's credentials: %w", err)
	}
	if _, err := gitsync.CommitAll(ctx, opts.GitOpsRepoPath, "urbex teardown"); err != nil {
		return fmt.Errorf("committing the gitops repo: %w", err)
	}
	fmt.Fprintln(o.Out, "Platform destroyed.")
	fmt.Fprintf(o.Out, "Keep %s: 'urbex bootstrap' creates a new platform and pushes it to its Gitea.\n", opts.GitOpsRepoPath)
	return nil
}

// remainingProjects lists the project environments the GitOps repo still
// has: folders under environments/, and LXCs in the allocation ledger.
func remainingProjects(gitopsRepoPath string) ([]string, error) {
	found := map[string]bool{}
	envs, err := os.ReadDir(filepath.Join(gitopsRepoPath, "environments"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		projects, err := os.ReadDir(filepath.Join(gitopsRepoPath, "environments", env.Name()))
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if p.IsDir() {
				found[p.Name()+" ("+env.Name()+")"] = true
			}
		}
	}
	ledger, err := allocation.Load(filepath.Join(gitopsRepoPath, "state", "allocations.json"), 0, 0)
	if err != nil {
		return nil, err
	}
	for _, hostname := range ledger.Hostnames() {
		found[hostname] = true
	}
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func baseHostnames(cfg *platform.Config) []string {
	services := BaseServicesFor(cfg)
	names := make([]string, 0, len(services))
	for _, svc := range services {
		names = append(names, Hostname(svc.Name))
	}
	return names
}
