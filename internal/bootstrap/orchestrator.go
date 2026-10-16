// Package bootstrap implements the provisioning phase of `urbex
// bootstrap`: it materializes the embedded base-service Terraform/Ansible
// assets into a GitOps repo working directory, then runs Terraform and
// Ansible against a Proxmox server to create Gitea, Komodo, Technitium,
// Keycloak, and the observability stack. See
// docs/decisions/0006-bootstrap-command.md and
// docs/decisions/0017-embedded-base-infra-assets.md in urbforge/urbex.
//
// After provisioning, it wires the base services together (Gitea tokens,
// org and GitOps repo, Komodo API key and accounts - see platform.go) and
// pushes the GitOps repo to Gitea. Configuring the Cloudflare tunnel is a
// later phase, not yet implemented here.
package bootstrap

import (
	"context"
	"fmt"
	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/keycloak"
	"github.com/urbforge/urbex-cli/internal/materialize"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/runner"
	"github.com/urbforge/urbex-cli/internal/secrets"
)

// ProxmoxLister is the subset of proxmoxapi.Client used for the
// pre-flight existence check.
type ProxmoxLister interface {
	ListContainerHostnames(ctx context.Context, node string) ([]string, error)
}

// Command, CommandRunner, and ExecRunner are aliases for the shared types
// in internal/runner, kept here so existing callers of this package don't
// need to change their imports.
type (
	Command       = runner.Command
	CommandRunner = runner.CommandRunner
	ExecRunner    = runner.ExecRunner
)

// Orchestrator runs the bootstrap provisioning flow.
type Orchestrator struct {
	Config      *platform.Config
	Credentials *credentials.Credentials
	Proxmox     ProxmoxLister
	Runner      CommandRunner
	Out         io.Writer
	// Clients builds the Gitea/Komodo API clients used after provisioning;
	// zero value means DefaultClients().
	Clients Clients
	// Secrets encrypts the platform Stacks' secrets; nil means a sops
	// store for the platform's age key.
	Secrets SecretWriter
}

// Options controls a single Run.
type Options struct {
	// GitOpsRepoPath is the local working directory of the GitOps repo
	// (already git-initialized, or about to be by a caller) that
	// bootstrap materializes Terraform/Ansible assets into.
	GitOpsRepoPath string
	// DryRun materializes assets and reports the plan without running
	// terraform or ansible-playbook.
	DryRun bool
}

// Run executes the bootstrap provisioning flow: pre-flight check,
// materialize assets, terraform apply, ansible-playbook.
func (o *Orchestrator) Run(ctx context.Context, opts Options) error {
	if errs := o.Config.Validate(); len(errs) > 0 {
		return fmt.Errorf("invalid platform config: %v", errs)
	}

	if missing := o.Credentials.MissingForProvisioning(); len(missing) > 0 {
		return fmt.Errorf("missing required credentials: %v", missing)
	}
	if o.Config.Cloudflare.Enabled() && o.Credentials.CloudflareToken == "" {
		return fmt.Errorf("missing required credentials: [URBEX_CLOUDFLARE_TOKEN] (urbex.platform.yaml sets cloudflare.accountId)")
	}
	// The age key encrypts every secret in the GitOps repo: better to
	// learn it is malformed now than after provisioning.
	if _, err := secrets.Recipient(o.Credentials.AgeKey); err != nil {
		return err
	}

	toCreate, alreadyPresent, err := o.checkExisting(ctx, opts.GitOpsRepoPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Base services already present on Proxmox: %v\n", alreadyPresent)
	fmt.Fprintf(o.Out, "Base services to provision: %v\n", toCreate)

	ips, prefixLen, err := allocateIPs(o.Config)
	if err != nil {
		return fmt.Errorf("allocating static IPs: %w", err)
	}
	ep, err := EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	// What the services are configured with: names, never IPs (ADR-0027).
	internal, err := InternalEndpointsFor(o.Config)
	if err != nil {
		return err
	}

	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("materializing base-service assets: %w", err)
	}

	tfDir := filepath.Join(opts.GitOpsRepoPath, "terraform", "base")
	// New LXCs are cloned from urbex's prepared template, built first when
	// missing or outdated (redemptor#18).
	cloneVMID := 0
	if !opts.DryRun {
		cloneVMID = o.ensureTemplate(ctx, opts.GitOpsRepoPath)
	}
	if err := writeTFVars(tfDir, o.Config, ips, prefixLen, cloneVMID); err != nil {
		return fmt.Errorf("writing terraform vars: %w", err)
	}

	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible")
	if err := writeInventory(ansibleDir, o.Config.Domain, internal, PublicEndpointsFor(o.Config), ips); err != nil {
		return fmt.Errorf("writing ansible inventory: %w", err)
	}

	if opts.DryRun {
		fmt.Fprintln(o.Out, "Dry run: assets materialized, terraform/ansible-playbook were not executed.")
		return nil
	}

	if err := ensureSecrets(o.Credentials); err != nil {
		return fmt.Errorf("generating platform secrets: %w", err)
	}
	tfEnv := []string{"TF_VAR_proxmox_api_token=" + o.Credentials.ProxmoxToken}

	if err := o.Runner.Run(ctx, Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: tfEnv}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := runner.TerraformChange(ctx, o.Runner, o.Out, tfDir, "apply", tfEnv); err != nil {
		return err
	}
	if cloneVMID > 0 {
		if err := runner.TerraformGrowClones(ctx, o.Runner, o.Out, tfDir, tfEnv); err != nil {
			return err
		}
	}
	// The LXCs just created have new host keys at addresses that may have
	// served others before (a teardown, an LXC deleted by hand).
	var created []string
	for _, hostname := range toCreate {
		created = append(created, ips[hostname])
	}
	if err := knownhosts.Forget(opts.GitOpsRepoPath, created...); err != nil {
		return fmt.Errorf("updating known hosts: %w", err)
	}

	clients := o.Clients
	if clients.Gitea == nil || clients.Komodo == nil || clients.SyncRepo == nil {
		clients = DefaultClients()
	}

	ansibleArgs := []string{"-i", "inventory.yml", "playbook.yml"}
	ansibleEnv := o.Credentials.Env(ansibleSecretEnv...)
	tunnelToken := ""
	if o.Config.Cloudflare.Enabled() {
		// The tunnel's connector token reaches its LXC as a secret of its
		// Stack, encrypted in the GitOps repo.
		var err error
		if tunnelToken, err = o.tunnelToken(ctx, clients); err != nil {
			return fmt.Errorf("setting up the cloudflare tunnel: %w", err)
		}
	}
	if err := o.Runner.Run(ctx, Command{Dir: ansibleDir, Name: "ansible-playbook", Args: ansibleArgs, Env: ansibleEnv}); err != nil {
		return fmt.Errorf("ansible-playbook: %w", err)
	}
	fmt.Fprintln(o.Out, "Base services provisioned.")

	if err := o.setupDNS(ctx, clients, ep, ips, opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("setting up technitium: %w", err)
	}
	// The platform services Komodo deploys from the GitOps repo: their
	// folders go in before setupPlatform pushes it.
	stacks := o.platformStacks(tunnelToken)
	if err := o.writePlatformStacks(ctx, opts.GitOpsRepoPath, stacks); err != nil {
		return err
	}
	if err := o.writeTelemetryAgents(opts.GitOpsRepoPath); err != nil {
		return err
	}
	if err := o.setupPlatform(ctx, clients, opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("setting up gitea/komodo: %w", err)
	}
	if err := o.connectPlatformServers(ctx, clients, ansibleDir, stacks); err != nil {
		return fmt.Errorf("connecting the base services to komodo: %w", err)
	}
	if o.Config.Cloudflare.Enabled() {
		if err := o.publishPlatform(ctx, clients); err != nil {
			return fmt.Errorf("publishing the platform on cloudflare: %w", err)
		}
	}
	// Keycloak last: Komodo has deployed it, but its first start takes
	// minutes, and the platform isn't ready - nor can apply set up a
	// realm - until it answers (urbforge/urbex-cli#32).
	if err := o.waitKeycloak(ctx, clients); err != nil {
		return err
	}
	path, _ := credentials.DefaultPath()
	fmt.Fprintf(o.Out, "Platform ready. Gitea/Komodo admin user: %s; generated passwords and tokens are in %s.\n", GiteaAdminUser, path)
	return nil
}

// checkExisting queries Proxmox for containers matching the expected
// base-service hostnames. If one exists but isn't yet tracked in local
// Terraform state, it aborts rather than risk adopting or duplicating an
// unrelated resource - see docs/decisions/0017-embedded-base-infra-assets.md.
func (o *Orchestrator) checkExisting(ctx context.Context, gitopsRepoPath string) (toCreate, alreadyPresent []string, err error) {
	existing, err := o.Proxmox.ListContainerHostnames(ctx, o.Config.Proxmox.Node)
	if err != nil {
		return nil, nil, fmt.Errorf("checking existing Proxmox containers: %w", err)
	}

	existingSet := make(map[string]bool, len(existing))
	for _, h := range existing {
		existingSet[h] = true
	}

	for _, svc := range BaseServicesFor(o.Config) {
		h := Hostname(svc.Name)
		if existingSet[h] {
			alreadyPresent = append(alreadyPresent, h)
		} else {
			toCreate = append(toCreate, h)
		}
	}

	if len(alreadyPresent) > 0 {
		statePath := filepath.Join(gitopsRepoPath, "terraform", "base", "terraform.tfstate")
		if _, statErr := os.Stat(statePath); os.IsNotExist(statErr) {
			return nil, nil, fmt.Errorf(
				"found existing Proxmox container(s) %v not tracked in %s - resolve manually (rename/remove them, or point --gitops-repo at the repo that already manages them) before running bootstrap",
				alreadyPresent, statePath,
			)
		}
	}

	return toCreate, alreadyPresent, nil
}

func allocateIPs(cfg *platform.Config) (map[string]string, int, error) {
	services := BaseServicesFor(cfg)
	addrs, prefixLen, err := netalloc.Allocate(cfg.Proxmox.Network.CIDR, cfg.Proxmox.Network.BaseHostOffset, len(services))
	if err != nil {
		return nil, 0, err
	}

	ips := make(map[string]string, len(services))
	for i, svc := range services {
		ips[Hostname(svc.Name)] = addrs[i]
	}
	return ips, prefixLen, nil
}

// waitKeycloak waits for Keycloak to answer on the LAN, saying so when it
// doesn't yet.
func (o *Orchestrator) waitKeycloak(ctx context.Context, clients Clients) error {
	ep, err := EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	ready := clients.KeycloakReady
	if ready == nil {
		ready = func(ctx context.Context, baseURL string, waiting func()) error {
			return keycloak.WaitReady(ctx, &http.Client{Timeout: 10 * time.Second}, baseURL, keycloak.StartTimeout, 5*time.Second, waiting)
		}
	}
	return ready(ctx, ep.KeycloakURL, func() {
		fmt.Fprintln(o.Out, "Waiting for Keycloak to start - its first start takes a few minutes...")
	})
}
