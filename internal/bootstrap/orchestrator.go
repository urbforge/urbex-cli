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
	"io"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/credentials"
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

	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("materializing base-service assets: %w", err)
	}

	tfDir := filepath.Join(opts.GitOpsRepoPath, "terraform", "base")
	if err := writeTFVars(tfDir, o.Config, ips, prefixLen); err != nil {
		return fmt.Errorf("writing terraform vars: %w", err)
	}

	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible")
	if err := writeInventory(ansibleDir, o.Config.Domain, ep, ips); err != nil {
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
	if err := o.Runner.Run(ctx, Command{Dir: tfDir, Name: "terraform", Args: []string{"apply", "-auto-approve"}, Env: tfEnv}); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	ansibleArgs := []string{"-i", "inventory.yml", "playbook.yml"}
	ansibleEnv := o.Credentials.Env(ansibleSecretEnv...)
	if err := o.Runner.Run(ctx, Command{Dir: ansibleDir, Name: "ansible-playbook", Args: ansibleArgs, Env: ansibleEnv}); err != nil {
		return fmt.Errorf("ansible-playbook: %w", err)
	}
	fmt.Fprintln(o.Out, "Base services provisioned.")

	clients := o.Clients
	if clients.Gitea == nil || clients.Komodo == nil || clients.SyncRepo == nil {
		clients = DefaultClients()
	}
	if err := o.setupPlatform(ctx, clients, opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("setting up gitea/komodo: %w", err)
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

	for _, svc := range BaseServices {
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
	addrs, prefixLen, err := netalloc.Allocate(cfg.Proxmox.Network.CIDR, cfg.Proxmox.Network.BaseHostOffset, len(BaseServices))
	if err != nil {
		return nil, 0, err
	}

	ips := make(map[string]string, len(BaseServices))
	for i, svc := range BaseServices {
		ips[Hostname(svc.Name)] = addrs[i]
	}
	return ips, prefixLen, nil
}
