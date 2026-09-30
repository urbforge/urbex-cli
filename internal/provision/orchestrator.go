// Package provision implements `urbex plan`/`urbex apply`: given a
// validated urbex.yaml and an environment, it allocates a VMID/IP for
// each declared service's LXC (one per service per environment, see
// docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex),
// materializes the generic per-service Terraform module into the GitOps
// repo, then runs terraform plan/apply. Apply then installs Docker and
// Komodo Periphery on the LXCs with ansible-playbook and hands the
// environment over to Komodo (internal/rollout), which builds and deploys
// it now and on every later push to the environment's branch.
//
// DNS registration, Keycloak realm/client provisioning, and ingress
// (Cloudflare Tunnel) setup are later phases, not yet implemented here -
// see the message printed at the end of a successful Apply.
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/materialize"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"github.com/urbforge/urbex-cli/internal/runner"
)

// ValidEnvs are the only environments urbex provisions in v1 - see
// docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.
var ValidEnvs = map[string]bool{"staging": true, "prod": true}

// Rollouter sets up and runs an environment's build-and-deploy pipeline
// on Komodo - see internal/rollout.
type Rollouter interface {
	Setup(ctx context.Context, t rollout.Target) (*rollout.Resources, error)
	Run(ctx context.Context, t rollout.Target) ([]rollout.Deployed, error)
	Remove(ctx context.Context, t rollout.Target) error
	RemoveServers(ctx context.Context, t rollout.Target) error
}

// Orchestrator runs the plan/apply provisioning flow for one project+env.
type Orchestrator struct {
	Manifest    *manifest.Manifest
	Config      *platform.Config
	Credentials *credentials.Credentials
	Runner      runner.CommandRunner
	Out         io.Writer
	// Rollout drives Komodo; required by Apply, Deploy, and Destroy.
	Rollout Rollouter
	// SyncGitOps commits and pushes the GitOps repo to Gitea after a
	// change; nil skips syncing.
	SyncGitOps func(ctx context.Context, gitopsRepoPath, message string) error
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

// Apply materializes assets, runs `terraform apply`, installs Docker and
// Komodo Periphery on the resulting LXCs, then sets up and runs the
// environment's rollout on Komodo (see release).
func (o *Orchestrator) Apply(ctx context.Context, opts Options) error {
	tfDir, err := o.prepare(opts)
	if err != nil {
		return err
	}
	if err := o.checkPlatform(); err != nil {
		return err
	}

	env := tfEnv(o.Credentials)
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: env}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"apply", "-auto-approve"}, Env: env}); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	ledger, err := o.loadLedger(opts)
	if err != nil {
		return err
	}
	allocations, _ := o.lookupAllocations(ledger, opts.Env)
	ep, err := bootstrap.EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env)
	if err := writeInventory(ansibleDir, o.Manifest.Project, opts.Env, o.Manifest.Services, allocations, ep); err != nil {
		return fmt.Errorf("writing ansible inventory: %w", err)
	}
	if err := o.runAnsible(ctx, opts); err != nil {
		return err
	}

	if err := o.release(ctx, opts, allocations, "apply"); err != nil {
		return err
	}
	fmt.Fprintln(o.Out, "Not yet implemented by urbex apply: DNS records in Technitium, the Keycloak")
	fmt.Fprintln(o.Out, "realm/clients, and Cloudflare Tunnel ingress - services are reachable on their LAN IPs.")
	return nil
}

// Deploy re-syncs the environment's Komodo resources from urbex.yaml
// (env vars, port, healthcheck, Dockerfile) and has Komodo build and
// deploy the head of the environment's branch - what a push to that
// branch does on its own. It touches neither Terraform nor the LXCs:
// every declared service must already have been applied.
func (o *Orchestrator) Deploy(ctx context.Context, opts Options) error {
	if !ValidEnvs[opts.Env] {
		return fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
	}
	if len(o.Manifest.Services) == 0 {
		return fmt.Errorf("urbex.yaml declares no services - nothing to deploy")
	}
	if err := o.checkPlatform(); err != nil {
		return err
	}

	ledger, err := o.loadLedger(opts)
	if err != nil {
		return err
	}
	allocations, unprovisioned := o.lookupAllocations(ledger, opts.Env)
	if len(unprovisioned) > 0 {
		return fmt.Errorf("service(s) not yet provisioned: %v - run 'urbex apply %s' first", unprovisioned, opts.Env)
	}
	return o.release(ctx, opts, allocations, "deploy")
}

func (o *Orchestrator) checkPlatform() error {
	if o.Rollout == nil {
		return fmt.Errorf("no rollout configured - run 'urbex bootstrap' first")
	}
	if missing := o.Credentials.MissingForPlatform(); len(missing) > 0 {
		return fmt.Errorf("missing platform credentials %v - run 'urbex bootstrap' on this machine first", missing)
	}
	return nil
}

func (o *Orchestrator) target(env string) rollout.Target {
	return rollout.Target{Project: o.Manifest.Project, Env: env, Services: o.Manifest.Services}
}

// release declares the environment's Builds, Deployments, Procedure and
// webhook (so every later push to its branch deploys on its own), runs
// the Procedure once now, and records the declared resources in the
// GitOps repo before pushing it to Gitea.
func (o *Orchestrator) release(ctx context.Context, opts Options, allocations map[string]allocation.Entry, verb string) error {
	target := o.target(opts.Env)
	resources, err := o.Rollout.Setup(ctx, target)
	if err != nil {
		return err
	}
	if err := writeResources(opts.GitOpsRepoPath, target, resources); err != nil {
		return fmt.Errorf("recording komodo resources: %w", err)
	}
	deployed, runErr := o.Rollout.Run(ctx, target)
	if o.SyncGitOps != nil {
		msg := fmt.Sprintf("urbex %s %s %s", verb, o.Manifest.Project, opts.Env)
		if err := o.SyncGitOps(ctx, opts.GitOpsRepoPath, msg); err != nil && runErr == nil {
			return fmt.Errorf("deployed, but syncing the gitops repo failed: %w", err)
		}
	}
	if runErr != nil {
		return runErr
	}

	branch := rollout.Branch(opts.Env)
	fmt.Fprintf(o.Out, "Deployed %s (%s) from branch %s:\n", o.Manifest.Project, opts.Env, branch)
	for i, svc := range o.Manifest.Services {
		entry := allocations[Hostname(o.Manifest.Project, svc.Name, opts.Env)]
		fmt.Fprintf(o.Out, "  %-16s http://%s:%d  %s (%s)\n", svc.Name, entry.IP, svc.Port, deployed[i].State, deployed[i].Image)
	}
	fmt.Fprintf(o.Out, "From now on, merging into %s deploys %s automatically.\n", branch, opts.Env)
	return nil
}

// writeResources records the Komodo resources urbex declared for a
// project environment at komodo/<project>/<env>.json in the GitOps repo.
func writeResources(gitopsRepoPath string, t rollout.Target, res *rollout.Resources) error {
	dir := filepath.Join(gitopsRepoPath, "komodo", t.Project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, t.Env+".json"), append(data, '\n'), 0o644)
}

func (o *Orchestrator) loadLedger(opts Options) (*allocation.Ledger, error) {
	ledgerPath := filepath.Join(opts.GitOpsRepoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, o.Config.Proxmox.VMIDBase+100, o.Config.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return nil, fmt.Errorf("loading allocation ledger: %w", err)
	}
	return ledger, nil
}

// lookupAllocations returns the ledger entries of every declared service
// in env, plus the hostnames of those that have none.
func (o *Orchestrator) lookupAllocations(ledger *allocation.Ledger, env string) (map[string]allocation.Entry, []string) {
	allocations := make(map[string]allocation.Entry, len(o.Manifest.Services))
	var missing []string
	for _, svc := range o.Manifest.Services {
		hostname := Hostname(o.Manifest.Project, svc.Name, env)
		entry, ok := ledger.Lookup(hostname)
		if !ok {
			missing = append(missing, hostname)
			continue
		}
		allocations[hostname] = entry
	}
	return allocations, missing
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

	// Komodo first, while the LXCs can still answer: the webhook, the
	// Procedure, and each service's Deployment and Build. Their Servers
	// go after the LXCs, or the agents would just register again.
	target := rollout.Target{Project: o.Manifest.Project, Env: opts.Env, Services: toDestroy}
	if o.Rollout != nil {
		if err := o.Rollout.Remove(ctx, target); err != nil {
			return fmt.Errorf("removing komodo resources: %w", err)
		}
		record := filepath.Join(opts.GitOpsRepoPath, "komodo", o.Manifest.Project, opts.Env+".json")
		if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
			return err
		}
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
	if o.Rollout != nil {
		if err := o.Rollout.RemoveServers(ctx, target); err != nil {
			return fmt.Errorf("removing komodo servers: %w", err)
		}
	}

	for _, hostname := range hostnames {
		ledger.Remove(hostname)
	}
	if err := ledger.Save(ledgerPath); err != nil {
		return fmt.Errorf("saving allocation ledger: %w", err)
	}
	if o.SyncGitOps != nil {
		msg := fmt.Sprintf("urbex destroy %s %s", o.Manifest.Project, opts.Env)
		if err := o.SyncGitOps(ctx, opts.GitOpsRepoPath, msg); err != nil {
			return fmt.Errorf("destroyed, but syncing the gitops repo failed: %w", err)
		}
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
		Env:  o.Credentials.Env("URBEX_KOMODO_ONBOARDING_KEY"),
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

	return tfDir, nil
}

// Hostname is the LXC hostname for one service in one environment, e.g.
// ("my-app", "api", "staging") -> "my-app-api-staging".
func Hostname(project, service, env string) string {
	return rollout.ResourceName(project, service, env)
}

func tfEnv(creds *credentials.Credentials) []string {
	return []string{"TF_VAR_proxmox_api_token=" + creds.ProxmoxToken}
}
