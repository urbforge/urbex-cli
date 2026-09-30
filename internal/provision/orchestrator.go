// Package provision implements urbex plan/apply/deploy/promote/destroy
// for one project: given a validated urbex.yaml and an environment, it
// allocates a VMID/IP for each declared service's LXC (one per service
// per environment, see docs/decisions/0009-one-lxc-per-service-per-env.md
// in urbforge/urbex), runs Terraform and Ansible (Docker + Komodo
// Periphery), and maintains the project's Builds and the environment's
// folder and Stacks (internal/rollout, internal/envrepo).
//
// What an environment runs is decided by the GitOps repo alone: deploy
// and promote only change version files there and push; Komodo does the
// rest. See docs/decisions/0020-trunk-releases-gitops-environments.md.
//
// DNS registration, Keycloak realm/client provisioning, and ingress
// (Cloudflare Tunnel) setup are later phases, not yet implemented here.
package provision

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/materialize"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"github.com/urbforge/urbex-cli/internal/runner"
	"github.com/urbforge/urbex-cli/internal/semver"
)

// ValidEnvs are the only environments urbex provisions in v1 - see
// docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.
var ValidEnvs = map[string]bool{"staging": true, "prod": true}

// Rollouter maintains a project's Builds and an environment's Stacks on
// Komodo, and reports what they run - see internal/rollout.
type Rollouter interface {
	SetupProject(ctx context.Context, project string, services []manifest.Service) error
	SetupEnv(ctx context.Context, t rollout.Target, repo envrepo.Repo) error
	WriteEnv(t rollout.Target, repo envrepo.Repo) error
	Deploy(ctx context.Context, t rollout.Target) error
	Reconcile(ctx context.Context) error
	MissingImages(ctx context.Context, project string, services []manifest.Service, version string) ([]string, error)
	WaitDeployed(ctx context.Context, t rollout.Target, repo envrepo.Repo) ([]rollout.Deployed, error)
	RemoveEnv(ctx context.Context, t rollout.Target) error
	RemoveServers(ctx context.Context, t rollout.Target) error
	RemoveProject(ctx context.Context, project string, services []manifest.Service) error
}

// Orchestrator runs the plan/apply provisioning flow for one project+env.
type Orchestrator struct {
	Manifest    *manifest.Manifest
	Config      *platform.Config
	Credentials *credentials.Credentials
	Runner      runner.CommandRunner
	Out         io.Writer
	// Rollout drives Komodo; required by everything but Plan.
	Rollout Rollouter
	// SyncGitOps commits and pushes the GitOps repo to Gitea after a
	// change - which is also what makes Komodo deploy; nil skips it.
	SyncGitOps func(ctx context.Context, gitopsRepoPath, message string) error
}

// Options controls a single run.
type Options struct {
	Env string
	// Version is the release Deploy sets the environment to ("1.4.2").
	Version string
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
// Komodo Periphery on the resulting LXCs, makes the project releasable
// (repo, Builds, release webhook) and the environment deployable (its
// folder in the GitOps repo, its Stacks). If the environment already has
// a version set, it waits for Komodo to run it; a new environment stays
// empty until `urbex deploy` gives it one.
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

	if err := o.Rollout.SetupProject(ctx, o.Manifest.Project, o.Manifest.Services); err != nil {
		return err
	}
	target, repo := o.target(opts.Env), envrepo.Repo{Path: opts.GitOpsRepoPath}
	if err := o.Rollout.SetupEnv(ctx, target, repo); err != nil {
		return err
	}
	if err := o.sync(ctx, opts, "apply"); err != nil {
		return err
	}

	versions, err := o.versions(repo, opts.Env)
	if err != nil {
		return err
	}
	if len(versions) < len(o.Manifest.Services) {
		fmt.Fprintf(o.Out, "%s (%s) is ready, with no version to run yet:\n", o.Manifest.Project, opts.Env)
		for _, svc := range o.Manifest.Services {
			entry := allocations[Hostname(o.Manifest.Project, svc.Name, opts.Env)]
			fmt.Fprintf(o.Out, "  %-16s http://%s:%d\n", svc.Name, entry.IP, svc.Port)
		}
		fmt.Fprintf(o.Out, "Release one with 'urbex release', then run 'urbex deploy %s'.\n", opts.Env)
	} else {
		// Nothing may have changed in the repo - e.g. re-applying onto a
		// recreated LXC - so tell Komodo to deploy rather than wait for it
		// to notice.
		if err := o.Rollout.Deploy(ctx, target); err != nil {
			return err
		}
		if err := o.waitAndReport(ctx, opts, allocations); err != nil {
			return err
		}
	}
	fmt.Fprintln(o.Out, "Not yet implemented by urbex apply: DNS records in Technitium, the Keycloak")
	fmt.Fprintln(o.Out, "realm/clients, and Cloudflare Tunnel ingress - services are reachable on their LAN IPs.")
	return nil
}

// Deploy sets the environment to opts.Version: it writes the version
// into each service's folder of the GitOps repo (re-generating the
// compose files from urbex.yaml on the way) and pushes. That push is
// what deploys - Komodo notices the changed folders - so Deploy then
// only waits for the environment to run the version.
func (o *Orchestrator) Deploy(ctx context.Context, opts Options) error {
	allocations, repo, err := o.applied(opts)
	if err != nil {
		return err
	}
	if _, ok := semver.Parse(opts.Version); !ok || opts.Version[0] == 'v' {
		return fmt.Errorf("invalid version %q: expected X.Y.Z", opts.Version)
	}
	missing, err := o.Rollout.MissingImages(ctx, o.Manifest.Project, o.Manifest.Services, opts.Version)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("release %s has no image for %v - was it released ('urbex release'), and did its build succeed?", opts.Version, missing)
	}

	if err := o.Rollout.WriteEnv(o.target(opts.Env), repo); err != nil {
		return err
	}
	for _, svc := range o.Manifest.Services {
		if err := repo.SetVersion(opts.Env, o.Manifest.Project, svc.Name, opts.Version); err != nil {
			return err
		}
	}
	if err := o.sync(ctx, opts, "deploy "+opts.Version+" to"); err != nil {
		return err
	}
	return o.waitAndReport(ctx, opts, allocations)
}

// Publish pushes a change made to the environment's folder outside of a
// version - a secret, say - and makes sure Komodo acts on it, so the
// services concerned are redeployed.
func (o *Orchestrator) Publish(ctx context.Context, opts Options, verb string) error {
	if _, _, err := o.applied(opts); err != nil {
		return err
	}
	if err := o.sync(ctx, opts, verb); err != nil {
		return err
	}
	// Unlike a version, there is nothing to watch for afterwards: ask
	// Komodo explicitly rather than rely on the webhook alone. The change
	// is pushed either way, so a failure here is not a failure to publish.
	if err := o.Rollout.Reconcile(ctx); err != nil {
		fmt.Fprintf(o.Out, "Warning: pushed, but could not ask Komodo to reconcile (%v); it does so on its own within 15 minutes.\n", err)
	}
	return nil
}

// Promote sets prod to the versions staging is set to, the same way
// Deploy does. It reports false, and changes nothing, if prod already
// is.
func (o *Orchestrator) Promote(ctx context.Context, gitopsRepoPath string) (bool, error) {
	from := Options{Env: "staging", GitOpsRepoPath: gitopsRepoPath}
	to := Options{Env: "prod", GitOpsRepoPath: gitopsRepoPath}
	if _, _, err := o.applied(from); err != nil {
		return false, err
	}
	allocations, repo, err := o.applied(to)
	if err != nil {
		return false, err
	}
	staging, err := o.versions(repo, from.Env)
	if err != nil {
		return false, err
	}
	prod, err := o.versions(repo, to.Env)
	if err != nil {
		return false, err
	}
	changed := false
	for _, svc := range o.Manifest.Services {
		version, ok := staging[svc.Name]
		if !ok {
			return false, fmt.Errorf("staging has no version set for %s - nothing to promote", svc.Name)
		}
		if prod[svc.Name] == version {
			continue
		}
		if err := repo.SetVersion(to.Env, o.Manifest.Project, svc.Name, version); err != nil {
			return false, err
		}
		fmt.Fprintf(o.Out, "%s: %s -> %s\n", svc.Name, orNone(prod[svc.Name]), version)
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err := o.sync(ctx, to, "promote staging to"); err != nil {
		return false, err
	}
	return true, o.waitAndReport(ctx, to, allocations)
}

func orNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}

// applied checks that every declared service has been applied to the
// environment - an LXC allocation and a folder in the GitOps repo.
func (o *Orchestrator) applied(opts Options) (map[string]allocation.Entry, envrepo.Repo, error) {
	repo := envrepo.Repo{Path: opts.GitOpsRepoPath}
	if !ValidEnvs[opts.Env] {
		return nil, repo, fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
	}
	if len(o.Manifest.Services) == 0 {
		return nil, repo, fmt.Errorf("urbex.yaml declares no services - nothing to deploy")
	}
	if err := o.checkPlatform(); err != nil {
		return nil, repo, err
	}
	ledger, err := o.loadLedger(opts)
	if err != nil {
		return nil, repo, err
	}
	allocations, unprovisioned := o.lookupAllocations(ledger, opts.Env)
	if len(unprovisioned) > 0 || !repo.Exists(opts.Env, o.Manifest.Project) {
		return nil, repo, fmt.Errorf("%s is not applied to %s yet (%v) - run 'urbex apply %s' first",
			o.Manifest.Project, opts.Env, unprovisioned, opts.Env)
	}
	return allocations, repo, nil
}

// versions returns the version each service is set to in env; services
// with none are absent.
func (o *Orchestrator) versions(repo envrepo.Repo, env string) (map[string]string, error) {
	versions := map[string]string{}
	for _, svc := range o.Manifest.Services {
		v, err := repo.Version(env, o.Manifest.Project, svc.Name)
		if err != nil {
			return nil, err
		}
		if v != "" {
			versions[svc.Name] = v
		}
	}
	return versions, nil
}

func (o *Orchestrator) sync(ctx context.Context, opts Options, verb string) error {
	if o.SyncGitOps == nil {
		return nil
	}
	msg := fmt.Sprintf("urbex %s %s %s", verb, o.Manifest.Project, opts.Env)
	if err := o.SyncGitOps(ctx, opts.GitOpsRepoPath, msg); err != nil {
		return fmt.Errorf("syncing the gitops repo: %w", err)
	}
	return nil
}

// waitAndReport waits for Komodo to run what the GitOps repo asks for,
// then prints where each service is.
func (o *Orchestrator) waitAndReport(ctx context.Context, opts Options, allocations map[string]allocation.Entry) error {
	fmt.Fprintf(o.Out, "Waiting for Komodo to deploy %s (%s) from the GitOps repo...\n", o.Manifest.Project, opts.Env)
	deployed, err := o.Rollout.WaitDeployed(ctx, o.target(opts.Env), envrepo.Repo{Path: opts.GitOpsRepoPath})
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Deployed %s (%s):\n", o.Manifest.Project, opts.Env)
	for i, svc := range o.Manifest.Services {
		entry := allocations[Hostname(o.Manifest.Project, svc.Name, opts.Env)]
		fmt.Fprintf(o.Out, "  %-16s http://%s:%d  %s (%s)\n", svc.Name, entry.IP, svc.Port, deployed[i].Want, deployed[i].Image)
	}
	return nil
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

	// Komodo first, while the LXCs can still answer: take the Stacks
	// down and delete them. Their Servers go after the LXCs, or the
	// agents would just register again.
	target := rollout.Target{Project: o.Manifest.Project, Env: opts.Env, Services: toDestroy}
	repo := envrepo.Repo{Path: opts.GitOpsRepoPath}
	if o.Rollout != nil {
		if err := o.Rollout.RemoveEnv(ctx, target); err != nil {
			return fmt.Errorf("removing komodo stacks: %w", err)
		}
	}
	if err := repo.RemoveProject(opts.Env, o.Manifest.Project); err != nil {
		return err
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
		// The Builds and the release webhook belong to the project, not
		// to an environment: they go with its last environment.
		remaining, err := repo.Environments(o.Manifest.Project)
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			if err := o.Rollout.RemoveProject(ctx, o.Manifest.Project, o.Manifest.Services); err != nil {
				return fmt.Errorf("removing komodo builds: %w", err)
			}
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
		Env:  o.Credentials.Env("URBEX_KOMODO_ONBOARDING_KEY", "URBEX_AGE_KEY"),
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
