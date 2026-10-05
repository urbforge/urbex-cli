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
package provision

import (
	"context"
	"fmt"
	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/gitea"
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
	BuildMain(ctx context.Context, project string) error
	Releases(ctx context.Context, project string) ([]gitea.Tag, error)
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
	// Proxmox, if set, lets plan/apply/destroy drop LXCs deleted outside
	// Terraform from its state (see forgetVanished).
	Proxmox ContainerLister
	// Keycloak keeps the project's realm and its frontends' clients; nil
	// skips it. KeycloakURL is Keycloak's LAN address.
	Keycloak    KeycloakAPI
	KeycloakURL string
	// Secrets encrypts the staging test users' passwords; nil means sops
	// with the platform's age key.
	Secrets SecretStore
	// Cloudflare publishes public services (manifest public: true) when
	// the platform has Cloudflare configured; nil disables publishing.
	Cloudflare CloudflareAPI
	// QuickTunnel looks up a public service's Cloudflare Quick Tunnel
	// hostname (cloudflare.quickTunnel, ADR-0025); nil disables it, same
	// as Cloudflare disables the named tunnel.
	QuickTunnel QuickTunnelLookup
	// Technitium gives the environment's services internal DNS records
	// (ADR-0026), unconditionally - unlike Cloudflare, nil just means it
	// wasn't wired up (no credentials yet), not that it is disabled.
	Technitium TechnitiumAPI
	Out        io.Writer
	// Rollout drives Komodo; required by everything but Plan.
	Rollout Rollouter
	// SyncGitOps commits and pushes the GitOps repo to Gitea after a
	// change - which is also what makes Komodo deploy; nil skips it.
	SyncGitOps func(ctx context.Context, gitopsRepoPath, message string) error
	// RefreshGitOps brings the local GitOps repo up to date with Gitea,
	// where Komodo also commits (an environment following main); nil
	// skips it.
	RefreshGitOps func(ctx context.Context, gitopsRepoPath string) error
}

// Options controls a single run.
type Options struct {
	Env string
	// Version is what Deploy sets the environment to: a release
	// ("1.4.2") or a commit of main ("a1b2c3d").
	Version string
	// FollowMain makes Deploy set the environment to follow main instead.
	FollowMain bool
	// GitOpsRepoPath is the local working directory of the GitOps repo,
	// already bootstrapped (see internal/bootstrap).
	GitOpsRepoPath string
}

// Plan materializes assets and runs `terraform plan`, without applying
// anything or running Ansible.
func (o *Orchestrator) Plan(ctx context.Context, opts Options) error {
	// The ledger is shared: allocate from what Gitea has, not from a stale
	// copy.
	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	tfDir, err := o.prepare(opts)
	if err != nil {
		return err
	}

	env := tfEnv(o.Credentials)
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"init"}, Env: env}); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if err := o.forgetVanished(ctx, tfDir, env); err != nil {
		return err
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
	if err := o.forgetVanished(ctx, tfDir, env); err != nil {
		return err
	}
	existed, err := o.existingContainers(ctx)
	if err != nil {
		return err
	}
	if err := runner.TerraformChange(ctx, o.Runner, o.Out, tfDir, "apply", env); err != nil {
		return err
	}

	ledger, err := o.loadLedger(opts)
	if err != nil {
		return err
	}
	allocations, _ := o.lookupAllocations(ledger, opts.Env)
	if existed != nil {
		// LXCs terraform just created have new host keys, possibly at an
		// address another LXC had before.
		var created []string
		for _, entry := range allocations {
			if !existed[entry.VMID] {
				created = append(created, entry.IP)
			}
		}
		if err := knownhosts.Forget(opts.GitOpsRepoPath, created...); err != nil {
			return fmt.Errorf("updating known hosts: %w", err)
		}
	}
	ep, err := bootstrap.EndpointsFor(o.Config)
	if err != nil {
		return err
	}
	ansibleDir := filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env)
	quickTunnel := o.Config.Cloudflare.QuickTunnel && !o.Config.Cloudflare.Enabled()
	if err := writeInventory(opts.GitOpsRepoPath, ansibleDir, o.Manifest.Project, opts.Env, o.Manifest.Services, allocations, ep, o.Manifest.ShipsLogs(), quickTunnel); err != nil {
		return fmt.Errorf("writing ansible inventory: %w", err)
	}
	if err := o.runAnsible(ctx, opts); err != nil {
		return err
	}

	if err := o.Rollout.SetupProject(ctx, o.Manifest.Project, o.Manifest.Deployables()); err != nil {
		return err
	}
	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	target, repo := o.target(opts.Env), envrepo.Repo{Path: opts.GitOpsRepoPath}
	if err := o.Rollout.SetupEnv(ctx, target, repo); err != nil {
		return err
	}
	if err := o.sync(ctx, opts, "apply"); err != nil {
		return err
	}
	if err := o.publish(ctx, opts.Env, allocations); err != nil {
		return fmt.Errorf("publishing on cloudflare: %w", err)
	}
	if err := o.registerDNS(ctx, opts.Env, allocations); err != nil {
		return fmt.Errorf("registering internal dns: %w", err)
	}
	usersChanged, err := o.setupIdentity(ctx, opts)
	if err != nil {
		return fmt.Errorf("setting up keycloak: %w", err)
	}
	if usersChanged {
		if err := o.sync(ctx, opts, "keycloak test users of"); err != nil {
			return err
		}
	}

	versions, err := o.versions(repo, opts.Env)
	if err != nil {
		return err
	}
	set, tracking := 0, 0
	for _, v := range versions {
		if v.Version != "" {
			set++
		}
		if v.Track {
			tracking++
		}
	}
	if set < len(o.Manifest.Deployables()) && tracking == len(o.Manifest.Deployables()) {
		// A new environment that follows main: build main now, as a
		// push would, rather than wait for the next one.
		if err := o.Rollout.BuildMain(ctx, o.Manifest.Project); err != nil {
			return err
		}
		if err := o.refresh(ctx, opts); err != nil {
			return err
		}
		if err := o.waitAndReport(ctx, opts, allocations); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "%s follows main: every push to main is deployed here.\n", opts.Env)
	} else if set < len(o.Manifest.Deployables()) {
		fmt.Fprintf(o.Out, "%s (%s) is ready, with no version to run yet:\n", o.Manifest.Project, opts.Env)
		for _, svc := range o.Manifest.Deployables() {
			fmt.Fprintf(o.Out, "  %-16s %s\n", svc.Name, o.address(opts.Env, svc, allocations))
		}
		if opts.Env == "prod" {
			fmt.Fprintln(o.Out, "Give it staging's release with 'urbex release' (if staging runs an unreleased commit), then 'urbex promote'.")
		} else {
			fmt.Fprintf(o.Out, "Set one with 'urbex deploy %s --version <release>' or '--follow-main'.\n", opts.Env)
		}
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
	if !o.Config.Cloudflare.Enabled() {
		fmt.Fprintln(o.Out, "No Cloudflare in urbex.platform.yaml: the services are reachable on their LAN addresses only.")
	}
	return nil
}

// Deploy sets what the environment runs: opts.Version - a release, or a
// commit of main - which also stops it following main; or, with
// opts.FollowMain, main's latest build, from now on. It writes each
// service's version file in the GitOps repo (re-generating the compose
// files from urbex.yaml on the way) and pushes. That push is what
// deploys - Komodo notices the changed folders - so Deploy then only
// waits for the environment to run it.
func (o *Orchestrator) Deploy(ctx context.Context, opts Options) error {
	allocations, repo, err := o.applied(opts)
	if err != nil {
		return err
	}
	if opts.FollowMain {
		return o.followMain(ctx, opts, repo, allocations)
	}
	if !ValidVersion(opts.Version) {
		return fmt.Errorf("invalid version %q: expected a release (X.Y.Z) or a commit of main (its short hash)", opts.Version)
	}
	missing, err := o.Rollout.MissingImages(ctx, o.Manifest.Project, o.Manifest.Deployables(), opts.Version)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s has no image for %v - a release must be released ('urbex release'), a commit built by a push to main", opts.Version, missing)
	}

	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	if err := o.Rollout.WriteEnv(o.target(opts.Env), repo); err != nil {
		return err
	}
	before, err := o.versions(repo, opts.Env)
	if err != nil {
		return err
	}
	wasTracking := false
	for _, svc := range o.Manifest.Deployables() {
		wasTracking = wasTracking || before[svc.Name].Track
		if err := repo.SetVersion(opts.Env, o.Manifest.Project, svc.Name, envrepo.VersionFileState{Version: opts.Version}); err != nil {
			return err
		}
	}
	if err := o.sync(ctx, opts, "deploy "+opts.Version+" to"); err != nil {
		return err
	}
	if err := o.waitAndReport(ctx, opts, allocations); err != nil {
		return err
	}
	if wasTracking {
		fmt.Fprintf(o.Out, "%s stays on %s and no longer follows main - 'urbex deploy %s --follow-main' resumes.\n", opts.Env, opts.Version, opts.Env)
	}
	return nil
}

// followMain sets the environment to follow main, and runs main's
// current head.
func (o *Orchestrator) followMain(ctx context.Context, opts Options, repo envrepo.Repo, allocations map[string]allocation.Entry) error {
	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	if err := o.Rollout.WriteEnv(o.target(opts.Env), repo); err != nil {
		return err
	}
	current, err := o.versions(repo, opts.Env)
	if err != nil {
		return err
	}
	for _, svc := range o.Manifest.Deployables() {
		st := envrepo.VersionFileState{Version: current[svc.Name].Version, Track: true}
		if err := repo.SetVersion(opts.Env, o.Manifest.Project, svc.Name, st); err != nil {
			return err
		}
	}
	if err := o.sync(ctx, opts, "follow main in"); err != nil {
		return err
	}
	// Bring it to main's head now rather than at the next push.
	if err := o.Rollout.BuildMain(ctx, o.Manifest.Project); err != nil {
		return err
	}
	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	if err := o.waitAndReport(ctx, opts, allocations); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "%s follows main: every push to main is deployed here.\n", opts.Env)
	return nil
}

// ValidVersion reports whether v can be deployed: a release (X.Y.Z) or
// the short hash of a commit (7 to 12 hex characters, as images of main
// are tagged).
func ValidVersion(v string) bool {
	if _, ok := semver.Parse(v); ok && !strings.HasPrefix(v, "v") {
		return true
	}
	return isCommit(v)
}

func isCommit(v string) bool {
	if len(v) < 7 || len(v) > 12 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Publish pushes a change made to the environment's folder outside of a
// version - a secret, say - and makes sure Komodo acts on it, so the
// services concerned are redeployed.
func (o *Orchestrator) Publish(ctx context.Context, opts Options, verb string) error {
	if _, _, err := o.applied(opts); err != nil {
		return err
	}
	if err := o.refresh(ctx, opts); err != nil {
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

// Promote sets prod to the release staging runs, the same way Deploy
// does. Staging usually runs a commit of main; prod only runs releases,
// so that commit must have been released ('urbex release' releases it).
// It reports false, and changes nothing, if prod already runs it.
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
	if err := o.refresh(ctx, to); err != nil {
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
	releases, err := o.Rollout.Releases(ctx, o.Manifest.Project)
	if err != nil {
		return false, err
	}

	changed := false
	for _, svc := range o.Manifest.Deployables() {
		version := staging[svc.Name].Version
		if version == "" {
			return false, fmt.Errorf("staging has no version set for %s - nothing to promote", svc.Name)
		}
		release, err := releaseOf(version, releases)
		if err != nil {
			return false, err
		}
		if prod[svc.Name].Version == release && !prod[svc.Name].Track {
			continue
		}
		if err := repo.SetVersion(to.Env, o.Manifest.Project, svc.Name, envrepo.VersionFileState{Version: release}); err != nil {
			return false, err
		}
		fmt.Fprintf(o.Out, "%s: %s -> %s\n", svc.Name, orNone(prod[svc.Name].Version), release)
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

// releaseOf returns the release a version is: itself if it is one, the
// release tagged on that commit if it is a commit.
func releaseOf(version string, releases []gitea.Tag) (string, error) {
	if v, ok := semver.Parse(version); ok {
		return v.String(), nil
	}
	var found []semver.Version
	for _, t := range releases {
		if strings.HasPrefix(t.Commit, version) {
			if v, ok := semver.Parse(t.Name); ok {
				found = append(found, v)
			}
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("staging runs commit %s, which is not a release - release it first ('urbex release' releases what staging runs), then promote", version)
	}
	latest := found[0]
	for _, v := range found[1:] {
		if v.After(latest) {
			latest = v
		}
	}
	return latest.String(), nil
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

// versions returns what each service's version file says in env.
func (o *Orchestrator) versions(repo envrepo.Repo, env string) (map[string]envrepo.VersionFileState, error) {
	versions := map[string]envrepo.VersionFileState{}
	for _, svc := range o.Manifest.Deployables() {
		v, err := repo.Version(env, o.Manifest.Project, svc.Name)
		if err != nil {
			return nil, err
		}
		versions[svc.Name] = v
	}
	return versions, nil
}

func (o *Orchestrator) refresh(ctx context.Context, opts Options) error {
	if o.RefreshGitOps == nil {
		return nil
	}
	if err := o.RefreshGitOps(ctx, opts.GitOpsRepoPath); err != nil {
		return fmt.Errorf("updating the gitops repo from gitea: %w", err)
	}
	return nil
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
	for i, svc := range o.Manifest.Deployables() {
		fmt.Fprintf(o.Out, "  %-16s %s  %s (%s)\n", svc.Name, o.address(opts.Env, svc, allocations), deployed[i].Want, deployed[i].Image)
		if host := o.PublicHostname(opts.Env, svc); host != "" && svc.Public {
			fmt.Fprintf(o.Out, "  %-16s https://%s\n", "", host)
		} else if o.QuickTunnel != nil && svc.Public && !svc.IsWeb() {
			o.reportQuickTunnel(ctx, Hostname(o.Manifest.Project, svc.Name, opts.Env), allocations)
		}
	}
	return nil
}

// reportQuickTunnel prints a public service's Cloudflare Quick Tunnel
// URL (ADR-0025), or why it couldn't be found - the connector on a
// freshly started container needs some time to register with
// Cloudflare's edge, hence the bounded wait.
func (o *Orchestrator) reportQuickTunnel(ctx context.Context, hostname string, allocations map[string]allocation.Entry) {
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url, err := o.QuickTunnel(qctx, allocations[hostname].IP)
	if err != nil {
		fmt.Fprintf(o.Out, "  %-16s (cloudflare quick tunnel not ready: %v)\n", "", err)
		return
	}
	fmt.Fprintf(o.Out, "  %-16s %s (cloudflare quick tunnel: random URL, no authentication, changes on every restart - never a substitute for cloudflare.accountId/zoneId in production, see ADR-0025)\n", "", url)
}

func (o *Orchestrator) checkPlatform() error {
	if o.Rollout == nil {
		return fmt.Errorf("no rollout configured - run 'urbex bootstrap' first")
	}
	if missing := o.Credentials.MissingForPlatform(); len(missing) > 0 {
		return fmt.Errorf("missing platform credentials %v - run 'urbex bootstrap' on this machine first", missing)
	}
	if o.Config.Cloudflare.Enabled() && o.Credentials.CloudflareToken == "" {
		return fmt.Errorf("missing required credentials: [URBEX_CLOUDFLARE_TOKEN] (urbex.platform.yaml sets cloudflare.accountId)")
	}
	if o.Manifest.Frontend != nil && o.Manifest.Frontend.Type == "web" && !o.Config.Cloudflare.Enabled() {
		return fmt.Errorf("urbex.yaml has a web frontend, which is deployed to Cloudflare: set cloudflare.accountId and zoneId in urbex.platform.yaml and run 'urbex bootstrap'")
	}
	return nil
}

func (o *Orchestrator) target(env string) rollout.Target {
	hostnames := map[string]string{}
	services := o.Manifest.Deployables()
	for i, svc := range services {
		if host := o.PublicHostname(env, svc); host != "" && (svc.Public || svc.IsWeb()) {
			hostnames[svc.Name] = host
		}
		if svc.Auth != nil && svc.Auth.Keycloak {
			services[i].Extra = o.keycloakEnv(env)
		}
	}
	return rollout.Target{Project: o.Manifest.Project, Env: env, Services: services, Hostnames: hostnames}
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

	if err := o.refresh(ctx, opts); err != nil {
		return err
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
	// The web frontend has no LXC, but a Stack and a Worker.
	stacks := target
	for _, svc := range o.Manifest.Deployables() {
		if svc.IsWeb() {
			stacks.Services = append(append([]manifest.Service{}, toDestroy...), svc)
		}
	}
	repo := envrepo.Repo{Path: opts.GitOpsRepoPath}
	if o.Rollout != nil {
		if err := o.Rollout.RemoveEnv(ctx, stacks); err != nil {
			return fmt.Errorf("removing komodo stacks: %w", err)
		}
	}
	if err := o.unpublish(ctx, opts.Env, toDestroy); err != nil {
		return fmt.Errorf("removing from cloudflare: %w", err)
	}
	if err := o.unregisterDNS(ctx, opts.Env, toDestroy, allocations); err != nil {
		return fmt.Errorf("removing internal dns: %w", err)
	}
	if err := o.unpublishWeb(ctx, opts.Env); err != nil {
		return fmt.Errorf("removing the web frontend from cloudflare: %w", err)
	}
	if err := o.teardownIdentity(ctx, opts.Env); err != nil {
		return fmt.Errorf("removing the keycloak realm: %w", err)
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
	if err := o.forgetVanished(ctx, tfDir, env); err != nil {
		return err
	}
	if err := runner.TerraformChange(ctx, o.Runner, o.Out, tfDir, "destroy", env); err != nil {
		return err
	}
	if o.Rollout != nil {
		if err := o.Rollout.RemoveServers(ctx, target); err != nil {
			return fmt.Errorf("removing komodo servers: %w", err)
		}
		// The Builds and the release webhook belong to the project, not
		// to an environment: they go with its last environment.
		envs, err := repo.Environments(o.Manifest.Project)
		if err != nil {
			return err
		}
		remaining := 0
		for _, env := range envs {
			if env != opts.Env {
				remaining++
			}
		}
		if remaining == 0 {
			if err := o.Rollout.RemoveProject(ctx, o.Manifest.Project, o.Manifest.Deployables()); err != nil {
				return fmt.Errorf("removing komodo builds: %w", err)
			}
		}
	}

	// The environment's folder goes last, right before the commit: the
	// release Action can still update it (a push to main) until then, and
	// a change to a file we delete would conflict when pulling. Its Stacks
	// are already gone, so nothing deploys it meanwhile.
	if err := o.refresh(ctx, opts); err != nil {
		return err
	}
	if err := repo.RemoveProject(opts.Env, o.Manifest.Project); err != nil {
		return err
	}
	// Terraform's state of the destroyed LXCs is empty now; it and the
	// environment's inventory are of no further use.
	for _, dir := range []string{
		tfDir,
		filepath.Join(opts.GitOpsRepoPath, "ansible", "projects", o.Manifest.Project, opts.Env),
	} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		os.Remove(filepath.Dir(dir)) // the project's directory, once it has no environment left
	}
	var destroyed []string
	for _, entry := range allocations {
		destroyed = append(destroyed, entry.IP)
	}
	if err := knownhosts.Forget(opts.GitOpsRepoPath, destroyed...); err != nil {
		return fmt.Errorf("updating known hosts: %w", err)
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

// existingContainers returns the VMIDs of the LXCs on the Proxmox node, or
// nil without a Proxmox client.
func (o *Orchestrator) existingContainers(ctx context.Context) (map[int]bool, error) {
	if o.Proxmox == nil {
		return nil, nil
	}
	ids, err := o.Proxmox.ListContainerIDs(ctx, o.Config.Proxmox.Node)
	if err != nil {
		return nil, fmt.Errorf("listing proxmox containers: %w", err)
	}
	existing := make(map[int]bool, len(ids))
	for _, id := range ids {
		existing[id] = true
	}
	return existing, nil
}

// address is where a service of the environment is reached: its LXC's
// LAN address, or a web frontend's public URL.
func (o *Orchestrator) address(env string, svc manifest.Service, allocations map[string]allocation.Entry) string {
	if svc.IsWeb() {
		return "https://" + o.PublicHostname(env, svc)
	}
	entry := allocations[Hostname(o.Manifest.Project, svc.Name, env)]
	return fmt.Sprintf("http://%s:%d", entry.IP, svc.Port)
}
