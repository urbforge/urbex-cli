// Package provision implements `urbex plan`/`urbex apply`: given a
// validated urbex.yaml and an environment, it allocates a VMID/IP for
// each declared service's LXC (one per service per environment, see
// docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex),
// materializes the generic per-service Terraform module and Ansible
// inventory into the GitOps repo, then runs terraform plan/apply and (for
// apply) ansible-playbook.
//
// DNS registration, Keycloak realm/client provisioning, and ingress
// (Cloudflare Tunnel) setup are later phases, not yet implemented here -
// see the message printed at the end of a successful Apply.
package provision

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/materialize"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/runner"
)

// ValidEnvs are the only environments urbex provisions in v1 - see
// docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.
var ValidEnvs = map[string]bool{"staging": true, "prod": true}

// Orchestrator runs the plan/apply provisioning flow for one project+env.
type Orchestrator struct {
	Manifest    *manifest.Manifest
	Config      *platform.Config
	Credentials *credentials.Credentials
	Runner      runner.CommandRunner
	Out         io.Writer
}

// Options controls a single Plan or Apply run.
type Options struct {
	Env string
	// GitOpsRepoPath is the local working directory of the GitOps repo,
	// already bootstrapped (see internal/bootstrap).
	GitOpsRepoPath string
}

// Plan materializes assets and runs `terraform plan`, without applying
// anything or running Ansible.
func (o *Orchestrator) Plan(ctx context.Context, opts Options) error {
	tfDir, err := o.prepare(opts)
	if err != nil {
		return err
	}

	env := tfEnv(o.Credentials)
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: env}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"plan"}, Env: env}); err != nil {
		return fmt.Errorf("terraform plan: %w", err)
	}
	return nil
}

// Apply materializes assets, runs `terraform apply`, then configures the
// resulting LXCs via ansible-playbook.
func (o *Orchestrator) Apply(ctx context.Context, opts Options) error {
	tfDir, err := o.prepare(opts)
	if err != nil {
		return err
	}

	env := tfEnv(o.Credentials)
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: env}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"apply", "-auto-approve"}, Env: env}); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	if err := o.runAnsible(ctx, opts); err != nil {
		return err
	}

	fmt.Fprintf(o.Out, "Service LXCs for %s (%s) provisioned.\n", o.Manifest.Project, opts.Env)
	fmt.Fprintln(o.Out, "Next (not yet implemented by urbex apply): register DNS records in Technitium,")
	fmt.Fprintln(o.Out, "provision the Keycloak realm/clients, and configure Cloudflare Tunnel ingress.")
	return nil
}

// Deploy re-runs Ansible against service LXCs that urbex apply already
// provisioned, to pick up urbex.yaml changes (env vars, port, image) -
// without touching Terraform/infrastructure. Every declared service must
// already have a VMID/IP allocation (see urbex apply); Deploy itself
// never allocates one.
//
// This runs ansible-playbook directly. It is not yet integrated with
// Komodo's GitOps-driven reconciliation - see
// docs/decisions/0004-gitops-gitea-komodo.md in urbforge/urbex.
func (o *Orchestrator) Deploy(ctx context.Context, opts Options) error {
	if !ValidEnvs[opts.Env] {
		return fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
	}
	if len(o.Manifest.Services) == 0 {
		return fmt.Errorf("urbex.yaml declares no services - nothing to deploy")
	}

	ledgerPath := filepath.Join(opts.GitOpsRepoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, o.Config.Proxmox.VMIDBase+100, o.Config.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return fmt.Errorf("loading allocation ledger: %w", err)
	}

	allocations := make(map[string]allocation.Entry, len(o.Manifest.Services))
	var unprovisioned []string
	for _, svc := range o.Manifest.Services {
		hostname := Hostname(o.Manifest.Project, svc.Name, opts.Env)
		entry, ok := ledger.Lookup(hostname)
		if !ok {
			unprovisioned = append(unprovisioned, hostname)
			continue
		}
		allocations[hostname] = entry
	}
	if len(unprovisioned) > 0 {
		return fmt.Errorf("service(s) not yet provisioned: %v - run 'urbex apply %s' first", unprovisioned, opts.Env)
	}

	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("materializing assets: %w", err)
	}

	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env)
	if err := writeInventory(ansibleDir, o.Manifest.Project, opts.Env, o.Manifest.Services, allocations); err != nil {
		return fmt.Errorf("writing ansible inventory: %w", err)
	}

	if err := o.runAnsible(ctx, opts); err != nil {
		return err
	}

	fmt.Fprintf(o.Out, "Redeployed %s (%s).\n", o.Manifest.Project, opts.Env)
	fmt.Fprintln(o.Out, "Note: this ran ansible-playbook directly against the existing LXCs - it is")
	fmt.Fprintln(o.Out, "not yet integrated with Komodo's GitOps reconciliation (docs/decisions/0004-gitops-gitea-komodo.md).")
	return nil
}

// Destroy runs `terraform destroy` for every declared service that has
// been applied (see urbex apply) to opts.Env, then removes their entries
// from the allocation ledger. Services that were never applied are
// silently skipped - there's nothing to destroy for them. If none of the
// manifest's services have been applied, Destroy is a no-op.
//
// This only tears down project-service infrastructure; base/platform
// services created by urbex bootstrap are out of scope.
func (o *Orchestrator) Destroy(ctx context.Context, opts Options) error {
	if !ValidEnvs[opts.Env] {
		return fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
	}
	if len(o.Manifest.Services) == 0 {
		return fmt.Errorf("urbex.yaml declares no services - nothing to destroy")
	}
	if missing := o.Credentials.MissingForProvisioning(); len(missing) > 0 {
		return fmt.Errorf("missing required credentials: %v", missing)
	}

	ledgerPath := filepath.Join(opts.GitOpsRepoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, o.Config.Proxmox.VMIDBase+100, o.Config.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return fmt.Errorf("loading allocation ledger: %w", err)
	}

	allocations := make(map[string]allocation.Entry)
	var toDestroy []manifest.Service
	var hostnames []string
	for _, svc := range o.Manifest.Services {
		hostname := Hostname(o.Manifest.Project, svc.Name, opts.Env)
		if entry, ok := ledger.Lookup(hostname); ok {
			allocations[hostname] = entry
			toDestroy = append(toDestroy, svc)
			hostnames = append(hostnames, hostname)
		}
	}

	if len(toDestroy) == 0 {
		fmt.Fprintf(o.Out, "Nothing to destroy: no services of %s (%s) have been applied.\n", o.Manifest.Project, opts.Env)
		return nil
	}

	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("materializing base assets: %w", err)
	}
	if err := materialize.ProjectTerraform(opts.GitOpsRepoPath, o.Manifest.Project, opts.Env); err != nil {
		return fmt.Errorf("materializing project terraform: %w", err)
	}

	_, prefixLen, err := netalloc.Allocate(o.Config.Proxmox.Network.CIDR, 0, 1)
	if err != nil {
		return fmt.Errorf("parsing network cidr: %w", err)
	}

	tfDir := filepath.Join(opts.GitOpsRepoPath, "terraform", "projects", o.Manifest.Project, opts.Env)
	if err := writeTFVars(tfDir, o.Config, o.Manifest.Project, opts.Env, toDestroy, allocations, prefixLen); err != nil {
		return fmt.Errorf("writing terraform vars: %w", err)
	}

	env := tfEnv(o.Credentials)
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: env}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"destroy", "-auto-approve"}, Env: env}); err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}

	for _, hostname := range hostnames {
		ledger.Remove(hostname)
	}
	if err := ledger.Save(ledgerPath); err != nil {
		return fmt.Errorf("saving allocation ledger: %w", err)
	}

	fmt.Fprintf(o.Out, "Destroyed %s (%s): %v.\n", o.Manifest.Project, opts.Env, hostnames)
	return nil
}

func (o *Orchestrator) runAnsible(ctx context.Context, opts Options) error {
	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env)
	if err := o.Runner.Run(ctx, runner.Command{
		Dir:  ansibleDir,
		Name: "ansible-playbook",
		Args: []string{"-i", "inventory.yml", filepath.Join(opts.GitOpsRepoPath, "ansible", "project-playbook.yml")},
	}); err != nil {
		return fmt.Errorf("ansible-playbook: %w", err)
	}
	return nil
}

// prepare validates inputs, allocates VMIDs/IPs, and materializes
// Terraform/Ansible assets - the steps shared by Plan and Apply. It
// returns the Terraform working directory for the project+env.
func (o *Orchestrator) prepare(opts Options) (string, error) {
	if !ValidEnvs[opts.Env] {
		return "", fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
	}
	if errs := o.Config.Validate(); len(errs) > 0 {
		return "", fmt.Errorf("invalid platform config: %v", errs)
	}
	if len(o.Manifest.Services) == 0 {
		return "", fmt.Errorf("urbex.yaml declares no services - nothing to provision (frontend deploys, e.g. Cloudflare Pages/Firebase, are not handled by urbex plan/apply)")
	}
	if missing := o.Credentials.MissingForProvisioning(); len(missing) > 0 {
		return "", fmt.Errorf("missing required credentials: %v", missing)
	}

	ledgerPath := filepath.Join(opts.GitOpsRepoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, o.Config.Proxmox.VMIDBase+100, o.Config.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return "", fmt.Errorf("loading allocation ledger: %w", err)
	}

	allocations := make(map[string]allocation.Entry, len(o.Manifest.Services))
	for _, svc := range o.Manifest.Services {
		hostname := Hostname(o.Manifest.Project, svc.Name, opts.Env)
		entry, err := ledger.Allocate(hostname, o.Config.Proxmox.Network.CIDR)
		if err != nil {
			return "", fmt.Errorf("allocating resources for %s: %w", hostname, err)
		}
		allocations[hostname] = entry
	}
	if err := ledger.Save(ledgerPath); err != nil {
		return "", fmt.Errorf("saving allocation ledger: %w", err)
	}

	if err := materialize.Assets(opts.GitOpsRepoPath); err != nil {
		return "", fmt.Errorf("materializing base assets: %w", err)
	}
	if err := materialize.ProjectTerraform(opts.GitOpsRepoPath, o.Manifest.Project, opts.Env); err != nil {
		return "", fmt.Errorf("materializing project terraform: %w", err)
	}

	_, prefixLen, err := netalloc.Allocate(o.Config.Proxmox.Network.CIDR, 0, 1)
	if err != nil {
		return "", fmt.Errorf("parsing network cidr: %w", err)
	}

	tfDir := filepath.Join(opts.GitOpsRepoPath, "terraform", "projects", o.Manifest.Project, opts.Env)
	if err := writeTFVars(tfDir, o.Config, o.Manifest.Project, opts.Env, o.Manifest.Services, allocations, prefixLen); err != nil {
		return "", fmt.Errorf("writing terraform vars: %w", err)
	}

	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env)
	if err := writeInventory(ansibleDir, o.Manifest.Project, opts.Env, o.Manifest.Services, allocations); err != nil {
		return "", fmt.Errorf("writing ansible inventory: %w", err)
	}

	return tfDir, nil
}

// Hostname is the LXC hostname for one service in one environment, e.g.
// ("my-app", "api", "staging") -> "my-app-api-staging".
func Hostname(project, service, env string) string {
	return project + "-" + service + "-" + env
}

func tfEnv(creds *credentials.Credentials) []string {
	return []string{"TF_VAR_proxmox_api_token=" + creds.ProxmoxToken}
}
