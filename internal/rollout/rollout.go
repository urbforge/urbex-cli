// Package rollout declares, in Komodo and Gitea, what makes a project
// build its releases and an environment run the versions the GitOps repo
// says:
//
//   - per service built from source, a Komodo Build from the project's
//     repo, run by the
//     platform's release Action - which a webhook on the project repo
//     triggers - for every push to main (an image per commit, which the
//     environments that follow main, staging by default, then run), and
//     tagged for every release (vX.Y.Z); an application from a published
//     image has none, its Stack pulls the image;
//   - per service and environment, a Komodo Stack on the service's LXC,
//     whose source is the service's folder under environments/ in the
//     GitOps repo - redeployed, when that folder changes, by the
//     platform's GitOps Procedure.
//
// See docs/decisions/0018-image-build-komodo-gitea-registry.md and
// docs/decisions/0020-trunk-releases-gitops-environments.md and
// docs/decisions/0021-staging-follows-main.md in urbforge/urbex.
package rollout

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/gitea"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/imageref"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/semver"
	platformassets "github.com/urbforge/urbex-cli/platform"
)

// GeneratedDockerfile is where a Build's pre-build step writes the Urbex
// Dockerfile for language runtimes, inside Komodo's clone of the repo.
const GeneratedDockerfile = "Dockerfile.urbex"

// ResourceName names a service's LXC, Komodo Server, and Stack in one
// environment, e.g. "my-app-api-staging".
func ResourceName(project, service, env string) string {
	return project + "-" + service + "-" + env
}

// BuildName names a service's Komodo Build and its image, e.g.
// "my-app-api". Builds don't belong to an environment: a release is
// built once and deployed everywhere.
func BuildName(project, service string) string { return project + "-" + service }

// Gitea is the subset of the Gitea client a rollout uses.
type Gitea interface {
	EnsureRepo(ctx context.Context, owner, name string) error
	BranchExists(ctx context.Context, owner, repo, branch string) (bool, error)
	EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, branchFilter string) error
	DeleteWebhook(ctx context.Context, owner, repo, targetURL string) error
	ListTags(ctx context.Context, owner, repo string) ([]gitea.Tag, error)
	BranchCommit(ctx context.Context, owner, repo, branch string) (string, error)
	ResolveCommit(ctx context.Context, owner, repo, ref string) (string, error)
	CreateTag(ctx context.Context, owner, repo, tag, target, message string) error
	ContainerImageExists(ctx context.Context, owner, name, tag string) (bool, error)
}

// Komodo is the subset of the Komodo client a rollout uses.
type Komodo interface {
	WaitServers(ctx context.Context, names []string, timeout time.Duration) (rekeyed []string, err error)
	EnsureBuild(ctx context.Context, name string, cfg komodo.BuildConfig) error
	EnsureStack(ctx context.Context, name string, cfg komodo.StackConfig) error
	ListStacks(ctx context.Context) (map[string]komodo.StackStatus, error)
	DeployStack(ctx context.Context, name string) (*komodo.Update, error)
	ExplainFailure(ctx context.Context, u *komodo.Update) string
	RunProcedure(ctx context.Context, name string) error
	RunAction(ctx context.Context, name string, args map[string]any) (*komodo.Update, error)
	DestroyStack(ctx context.Context, name string) error
	DeleteResource(ctx context.Context, kind, name string) error
	WaitIdle(ctx context.Context, builds []string, action string, timeout time.Duration) error
	TagResource(ctx context.Context, kind, name string, tags ...string) error
}

// Git is the subset of git operations a rollout uses on the app repo.
type Git interface {
	Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error
}

// Rollout manages projects and their environments through Gitea and
// Komodo.
type Rollout struct {
	Gitea  Gitea
	Komodo Komodo
	Git    Git
	// Endpoints is how the CLI reaches Gitea and Komodo (LAN addresses);
	// Internal, by internal DNS name, what Komodo and Gitea are
	// configured with: image names, git and registry providers, webhooks
	// (ADR-0027).
	Endpoints bootstrap.Endpoints
	Internal  bootstrap.Endpoints
	Org       string
	Token     string // Gitea token with write:repository
	// WebhookSecret is Komodo's webhook secret, which Gitea signs with.
	WebhookSecret string
	// RepoDir is the local working copy of the app repo, used only to
	// create the project's main branch on Gitea when it doesn't exist yet.
	RepoDir string
	Out     io.Writer
	// PollInterval is how often Komodo and the registry are polled while
	// waiting; 0 means 3s.
	PollInterval time.Duration
	// CacheWindow is how long Reconcile waits before asking Komodo to
	// look at the GitOps repo; 0 means 6s.
	CacheWindow time.Duration
	// NudgeAfter is how long to wait for a push to deploy on its own
	// before asking Komodo to reconcile; 0 means 12s.
	NudgeAfter time.Duration
	// Timeout bounds each wait (servers connecting, a release building,
	// an environment converging); 0 means 10 minutes.
	Timeout time.Duration
}

// Target is one environment of one project.
type Target struct {
	Project  string
	Env      string
	Services []manifest.Service
	// Hostnames are the public hostnames of the services that have one,
	// by service name; a web frontend is deployed to its own.
	Hostnames map[string]string
	// Logs: the telemetry agents ship the services' logs
	// (the manifest's observability.logs).
	Logs bool
	// Links, by service name, are the generated files and the wait of the
	// services that provide or use something (ADR-0028).
	Links map[string]*envrepo.Links
}

// lxcs are the target's services that run on an LXC of their own - all
// but a web frontend, whose Stack runs on the builder - and their LXCs.
func (t Target) lxcs() (services []manifest.Service, hosts []string) {
	for _, svc := range t.Services {
		if !svc.IsWeb() {
			services = append(services, svc)
			hosts = append(hosts, ResourceName(t.Project, svc.Name, t.Env))
		}
	}
	return services, hosts
}

// TelemetryHosts are the LXCs whose telemetry agents the target has.
func (t Target) TelemetryHosts() []string {
	_, hosts := t.lxcs()
	return hosts
}

// FollowsMainByDefault reports whether a new environment follows main:
// staging does - every push to main is deployed there - production
// doesn't. See docs/decisions/0021-staging-follows-main.md.
func FollowsMainByDefault(env string) bool { return env == "staging" }

// Deployed is the state of one service in an environment.
type Deployed struct {
	Service string
	// Want is the version the GitOps repo asks for: a release ("1.4.2"),
	// a commit of main ("a1b2c3d"), or "" if none is set.
	Want string
	// Track is whether the environment follows main.
	Track bool
	// Image is what Komodo reports running ("" if nothing is).
	Image string
	State string
}

func (r *Rollout) interval() time.Duration {
	if r.PollInterval == 0 {
		return 3 * time.Second
	}
	return r.PollInterval
}

// cacheWindow is how long Komodo can keep serving a stale view of a repo
// after pulling it (5 seconds), with a margin.
func (r *Rollout) cacheWindow() time.Duration {
	if r.CacheWindow == 0 {
		return 6 * time.Second
	}
	return r.CacheWindow
}

// nudgeAfter is how long WaitDeployed lets a push work on its own before
// asking Komodo to reconcile: comfortably more than Komodo's repo cache.
func (r *Rollout) nudgeAfter() time.Duration {
	if r.NudgeAfter == 0 {
		return 12 * time.Second
	}
	return r.NudgeAfter
}

func (r *Rollout) timeout() time.Duration {
	if r.Timeout == 0 {
		return 10 * time.Minute
	}
	return r.Timeout
}

// Image is a service's image reference without its tag: the image its
// Build pushes to Gitea's registry or, for an application from a
// published image, that image.
func (r *Rollout) Image(project string, svc manifest.Service) string {
	if svc.FromImage() {
		return svc.Image
	}
	return r.Internal.Registry + "/" + r.Org + "/" + BuildName(project, svc.Name)
}

// SetupProject makes a project releasable: its repo on Gitea (creating
// the main branch from the local HEAD if Gitea doesn't have it), a Build
// per service built from source, and the webhook that has Komodo build
// every release tag. It is idempotent and re-syncs the Builds from the
// manifest. A project of published images only has nothing to build: no
// Build and no webhook.
func (r *Rollout) SetupProject(ctx context.Context, project string, services []manifest.Service) error {
	if err := r.Gitea.EnsureRepo(ctx, r.Org, project); err != nil {
		return err
	}
	if err := r.ensureTrunk(ctx, project); err != nil {
		return err
	}
	built := manifest.Built(services)
	if len(built) == 0 {
		// The release Action fails for a repo without Builds.
		if err := r.deleteLANWebhook(ctx, project); err != nil {
			return err
		}
		return r.Gitea.DeleteWebhook(ctx, r.Org, project, r.Internal.ActionWebhook(bootstrap.ReleaseAction))
	}
	for _, svc := range built {
		cfg, err := r.buildConfig(project, svc)
		if err != nil {
			return err
		}
		if err := r.Komodo.EnsureBuild(ctx, BuildName(project, svc.Name), cfg); err != nil {
			return err
		}
		if err := r.Komodo.TagResource(ctx, "Build", BuildName(project, svc.Name), bootstrap.AppTag, project); err != nil {
			return err
		}
	}
	// Every push: the Action itself tells release tags from the rest.
	// Komodo is reached by name; a webhook set up by an older urbex,
	// by address, goes - both would deploy every push twice.
	if err := r.deleteLANWebhook(ctx, project); err != nil {
		return err
	}
	hook := r.Internal.ActionWebhook(bootstrap.ReleaseAction)
	return r.Gitea.EnsureWebhook(ctx, r.Org, project, hook, r.WebhookSecret, "*")
}

// deleteLANWebhook removes the project's release webhook to Komodo's LAN
// address, set up before the services reached each other by name
// (ADR-0027).
func (r *Rollout) deleteLANWebhook(ctx context.Context, project string) error {
	lan := r.Endpoints.ActionWebhook(bootstrap.ReleaseAction)
	if lan == r.Internal.ActionWebhook(bootstrap.ReleaseAction) {
		return nil
	}
	return r.Gitea.DeleteWebhook(ctx, r.Org, project, lan)
}

// ensureTrunk creates the project's main branch on Gitea from the local
// HEAD if Gitea doesn't have it yet. An existing branch is never
// touched.
func (r *Rollout) ensureTrunk(ctx context.Context, project string) error {
	exists, err := r.Gitea.BranchExists(ctx, r.Org, project, bootstrap.TrunkBranch)
	if err != nil || exists {
		return err
	}
	if r.RepoDir == "" {
		return fmt.Errorf("%s/%s has no %s branch on gitea - push it first", r.Org, project, bootstrap.TrunkBranch)
	}
	url := r.Endpoints.RepoURL(r.Org, project)
	if err := r.Git.Push(ctx, r.RepoDir, url, r.Token, "HEAD:refs/heads/"+bootstrap.TrunkBranch); err != nil {
		return fmt.Errorf("creating branch %s on gitea from %s: %w", bootstrap.TrunkBranch, r.RepoDir, err)
	}
	fmt.Fprintf(r.Out, "Created %s/%s on Gitea with branch %s from the local HEAD.\n", r.Org, project, bootstrap.TrunkBranch)
	return nil
}

// WriteEnv (re)generates what urbex owns in each service's folder of the
// GitOps repo - see envrepo.Repo.WriteService.
func (r *Rollout) WriteEnv(t Target, repo envrepo.Repo) error {
	for _, svc := range t.Services {
		follow := FollowsMainByDefault(t.Env) && !svc.FromImage()
		if err := repo.WriteService(t.Env, t.Project, svc, r.Image(t.Project, svc), follow, t.Links[svc.Name]); err != nil {
			return fmt.Errorf("writing %s: %w", envrepo.ServiceDir(t.Env, t.Project, svc.Name), err)
		}
	}
	// Each LXC's telemetry agent, a Stack of its own (ADR-0027).
	services, hosts := t.lxcs()
	for i, svc := range services {
		agent := bootstrap.TelemetryAgent{Host: hosts[i], Kind: bootstrap.AppTag, Service: svc.Name, Project: t.Project, Env: t.Env, Logs: t.Logs}
		if err := bootstrap.WriteTelemetryAgent(repo.Path, agent, r.Internal); err != nil {
			return err
		}
	}
	return nil
}

// SetupEnv makes an environment deployable from the GitOps repo: it
// writes each service's folder under environments/ and declares its
// Komodo Stack on the service's LXC. It deploys nothing by itself.
func (r *Rollout) SetupEnv(ctx context.Context, t Target, repo envrepo.Repo) error {
	names := make([]string, len(t.Services))
	var servers []string // a web frontend's Stack runs on the builder, not an LXC
	for i, svc := range t.Services {
		names[i] = ResourceName(t.Project, svc.Name, t.Env)
		if !svc.IsWeb() {
			servers = append(servers, names[i])
		}
	}
	rekeyed, err := r.Komodo.WaitServers(ctx, servers, r.timeout())
	for _, name := range rekeyed {
		fmt.Fprintf(r.Out, "Komodo server %s reconnected with a new key (its LXC was recreated): accepted.\n", name)
	}
	if err != nil {
		return fmt.Errorf("%w - is Periphery running on those LXCs? ('urbex apply %s' installs it)", err, t.Env)
	}

	if err := r.WriteEnv(t, repo); err != nil {
		return err
	}
	// Tagged as on Grafana: app, the project, the environment.
	tags := []string{bootstrap.AppTag, t.Project, t.Env}
	for _, server := range servers {
		if err := r.Komodo.TagResource(ctx, "Server", server, tags...); err != nil {
			return err
		}
	}
	for i, svc := range t.Services {
		if err := r.Komodo.EnsureStack(ctx, names[i], r.stackConfig(names[i], t, svc)); err != nil {
			return err
		}
		if err := r.Komodo.TagResource(ctx, "Stack", names[i], tags...); err != nil {
			return err
		}
	}
	for _, host := range servers {
		name := bootstrap.TelemetryStackName(host)
		if err := r.Komodo.EnsureStack(ctx, name, bootstrap.TelemetryStackConfig(host, r.Internal, r.Org)); err != nil {
			return err
		}
		if err := r.Komodo.TagResource(ctx, "Stack", name, bootstrap.AgentTag, t.Project, t.Env); err != nil {
			return err
		}
	}
	return nil
}

// DeployTelemetry has Komodo deploy the target's telemetry agents, from
// the GitOps repo as pushed.
func (r *Rollout) DeployTelemetry(ctx context.Context, t Target) error {
	for _, host := range t.TelemetryHosts() {
		name := bootstrap.TelemetryStackName(host)
		done, err := r.Komodo.DeployStack(ctx, name)
		if err != nil {
			return err
		}
		if !done.Success {
			return fmt.Errorf("komodo could not deploy stack %s - %s", name, r.Komodo.ExplainFailure(ctx, done))
		}
	}
	return nil
}

// Releases returns the project's releases: its vX.Y.Z tags on Gitea.
func (r *Rollout) Releases(ctx context.Context, project string) ([]gitea.Tag, error) {
	tags, err := r.Gitea.ListTags(ctx, r.Org, project)
	if err != nil {
		return nil, err
	}
	var releases []gitea.Tag
	for _, t := range tags {
		if _, ok := semver.Parse(t.Name); ok && strings.HasPrefix(t.Name, "v") {
			releases = append(releases, t)
		}
	}
	return releases, nil
}

// ResolveCommit returns the full hash of ref - a commit (abbreviated or
// not), branch, or tag - in the project's repo.
func (r *Rollout) ResolveCommit(ctx context.Context, project, ref string) (string, error) {
	return r.Gitea.ResolveCommit(ctx, r.Org, project, ref)
}

// MainCommit returns the full hash of the project's main branch head.
func (r *Rollout) MainCommit(ctx context.Context, project string) (string, error) {
	return r.Gitea.BranchCommit(ctx, r.Org, project, bootstrap.TrunkBranch)
}

// BuildMain has Komodo build the head of the project's main branch now
// and set the environments that follow main to it - what a push to main
// does - and waits for the build and the commit to the GitOps repo; the
// deploy then follows from there.
func (r *Rollout) BuildMain(ctx context.Context, project string) error {
	commit, err := r.MainCommit(ctx, project)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Building %s main@%s on Komodo...\n", project, shortRef(commit))
	args := map[string]any{
		"WEBHOOK_BRANCH": bootstrap.TrunkBranch,
		"WEBHOOK_BODY": map[string]any{
			"repository": map[string]any{"full_name": r.Org + "/" + project},
			"after":      commit,
		},
	}
	done, err := r.Komodo.RunAction(ctx, bootstrap.ReleaseAction, args)
	if err != nil {
		return err
	}
	if !done.Success {
		return fmt.Errorf("building %s main@%s failed - %s", project, shortRef(commit), r.Komodo.ExplainFailure(ctx, done))
	}
	return nil
}

// Release tags version on target (a full commit hash, or main) - which
// makes Komodo give that commit's images the version as tag, building
// them if main never did - and waits until every service has it.
func (r *Rollout) Release(ctx context.Context, project string, services []manifest.Service, version semver.Version, target string) error {
	message := "Release " + version.String()
	if err := r.Gitea.CreateTag(ctx, r.Org, project, version.Tag(), target, message); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Tagged %s on %s; Komodo is publishing its images...\n", version.Tag(), shortRef(target))

	deadline := time.Now().Add(r.timeout())
	for {
		missing, err := r.MissingImages(ctx, project, services, version.String())
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("release %s: no image yet for %v - check the %s Action and the Builds in Komodo (%s)",
				version, missing, bootstrap.ReleaseAction, r.Endpoints.KomodoURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.interval()):
		}
	}
}

func shortRef(ref string) string {
	if len(ref) == 40 {
		return ref[:7]
	}
	return ref
}

// MissingImages returns the services that have no image tagged version
// (a release, or a commit's short hash) in the registry.
func (r *Rollout) MissingImages(ctx context.Context, project string, services []manifest.Service, version string) ([]string, error) {
	var missing []string
	for _, svc := range services {
		ok, err := r.Gitea.ContainerImageExists(ctx, r.Org, BuildName(project, svc.Name), version)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, svc.Name)
		}
	}
	return missing, nil
}

// Deploy has Komodo deploy every Stack of t from the GitOps repo as
// pushed, unconditionally - for when nothing in the repo changed but the
// environment must be (re)created, as after urbex apply.
func (r *Rollout) Deploy(ctx context.Context, t Target) error {
	for _, svc := range t.Services {
		name := ResourceName(t.Project, svc.Name, t.Env)
		done, err := r.Komodo.DeployStack(ctx, name)
		if err != nil {
			return err
		}
		if !done.Success {
			return fmt.Errorf("komodo could not deploy stack %s - %s", name, r.Komodo.ExplainFailure(ctx, done))
		}
	}
	return nil
}

// Reconcile makes sure Komodo acts on a push to the GitOps repo that was
// just made: it asks Komodo to look at the repo and redeploy the Stacks
// whose folder changed - what the repo's webhook does on a push, and the
// Procedure's own schedule does periodically.
//
// The webhook alone isn't enough when pushes follow each other closely:
// Komodo caches a repo for a few seconds after pulling it, so the
// webhook of the second push can see the state of the first. Reconcile
// therefore waits out that window first; if the webhook did its job,
// this run finds nothing changed.
func (r *Rollout) Reconcile(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(r.cacheWindow()):
	}
	return r.Komodo.RunProcedure(ctx, bootstrap.GitOpsProcedure)
}

// WaitDeployed waits until Komodo runs, for every service of t, the
// version the GitOps repo asks for. Pushing the GitOps repo is what
// makes Komodo deploy; if the environment hasn't moved after a while -
// Komodo can miss a push that comes seconds after it last looked at the
// repo - WaitDeployed asks it to reconcile.
func (r *Rollout) WaitDeployed(ctx context.Context, t Target, repo envrepo.Repo) ([]Deployed, error) {
	deadline := time.Now().Add(r.timeout())
	nudge := time.Now().Add(r.nudgeAfter())
	for {
		deployed, err := r.Status(ctx, t, repo)
		if err != nil {
			return nil, err
		}
		var pending []string
		for _, d := range deployed {
			if d.Want == "" || d.State != "running" || !Runs(d.Image, d.Want) {
				pending = append(pending, fmt.Sprintf("%s (want %s, %s %s)", d.Service, orNone(d.Want), d.State, orNone(d.Image)))
			}
		}
		if len(pending) == 0 {
			return deployed, nil
		}
		if time.Now().After(nudge) {
			// Best effort: a failure here shows up as not converging.
			_ = r.Komodo.RunProcedure(ctx, bootstrap.GitOpsProcedure)
			nudge = time.Now().Add(2 * r.nudgeAfter())
		}
		if time.Now().After(deadline) {
			return deployed, fmt.Errorf("%s (%s) did not converge: %s - check the %s Procedure and the Stacks in Komodo (%s)",
				t.Project, t.Env, strings.Join(pending, "; "), bootstrap.GitOpsProcedure, r.Endpoints.KomodoURL)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.interval()):
		}
	}
}

// Runs reports whether image, as Komodo reports a Stack's, is version
// want: a tag, or a published image's tag and digest - which Docker
// reports by digest alone ("nginx@sha256:...").
func Runs(image, want string) bool {
	if strings.HasSuffix(image, ":"+want) {
		return true
	}
	pinned, ok := imageref.ParsePinned(want)
	return ok && strings.HasSuffix(image, "@"+pinned.Digest)
}

func orNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// Status returns, for every service of t, the version the GitOps repo
// asks for and what Komodo reports running.
func (r *Rollout) Status(ctx context.Context, t Target, repo envrepo.Repo) ([]Deployed, error) {
	stacks, err := r.Komodo.ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	deployed := make([]Deployed, 0, len(t.Services))
	for _, svc := range t.Services {
		st, err := repo.Version(t.Env, t.Project, svc.Name)
		if err != nil {
			return nil, err
		}
		d := Deployed{Service: svc.Name, Want: st.Version, Track: st.Track, State: "not_declared"}
		if st, ok := stacks[ResourceName(t.Project, svc.Name, t.Env)]; ok {
			d.State = st.State
			if len(st.Images) > 0 {
				d.Image = st.Images[0]
			}
		}
		deployed = append(deployed, d)
	}
	return deployed, nil
}

// RemoveEnv takes t's services down and deletes their Stacks. Call it
// before destroying the LXCs, while their agents can still do it, and
// RemoveServers after.
func (r *Rollout) RemoveEnv(ctx context.Context, t Target) error {
	names := make([]string, 0, len(t.Services)*2)
	for _, svc := range t.Services {
		names = append(names, ResourceName(t.Project, svc.Name, t.Env))
	}
	for _, host := range t.TelemetryHosts() {
		names = append(names, bootstrap.TelemetryStackName(host))
	}
	for _, name := range names {
		if err := r.Komodo.DestroyStack(ctx, name); err != nil {
			return err
		}
		if err := r.Komodo.DeleteResource(ctx, "Stack", name); err != nil {
			return err
		}
	}
	return nil
}

// RemoveServers deletes the Komodo Servers of t's LXCs. Call it once the
// LXCs are gone: a Periphery agent that is still running would simply
// register itself again with the onboarding key.
func (r *Rollout) RemoveServers(ctx context.Context, t Target) error {
	for _, svc := range t.Services {
		if err := r.Komodo.DeleteResource(ctx, "Server", ResourceName(t.Project, svc.Name, t.Env)); err != nil {
			return err
		}
	}
	return nil
}

// RemoveProject deletes a project's Builds and its release webhook, once
// no environment runs it anymore. The repo and its images stay on Gitea.
func (r *Rollout) RemoveProject(ctx context.Context, project string, services []manifest.Service) error {
	hook := r.Internal.ActionWebhook(bootstrap.ReleaseAction)
	if err := r.Gitea.DeleteWebhook(ctx, r.Org, project, hook); err != nil {
		return err
	}
	if err := r.deleteLANWebhook(ctx, project); err != nil {
		return err
	}
	// No push starts a build any more, but one may be running - a push
	// just before - and Komodo refuses to delete a busy Build.
	built := manifest.Built(services)
	if len(built) == 0 {
		return nil
	}
	builds := make([]string, 0, len(built))
	for _, svc := range built {
		builds = append(builds, BuildName(project, svc.Name))
	}
	if err := r.Komodo.WaitIdle(ctx, builds, bootstrap.ReleaseAction, r.timeout()); err != nil {
		return err
	}
	for _, svc := range built {
		if err := r.Komodo.DeleteResource(ctx, "Build", BuildName(project, svc.Name)); err != nil {
			return err
		}
	}
	return nil
}

// buildConfig is the Komodo Build of one service: clone the project's
// repo from Gitea, build with the service's own Dockerfile (runtime
// docker) or Urbex's per-runtime one, and push to Gitea's registry as
// <project>-<service>:<short commit hash>. The release Action sets the
// commit for each push to main and never rebuilds for a release: it
// adds the version tag to the commit's image.
func (r *Rollout) buildConfig(project string, svc manifest.Service) (komodo.BuildConfig, error) {
	cfg := komodo.BuildConfig{
		BuilderID:          bootstrap.KomodoServerName,
		GitProvider:        r.Internal.Registry, // Gitea's host:port serves both git and the registry
		GitHTTPS:           false,
		GitAccount:         bootstrap.GiteaAdminUser,
		Repo:               r.Org + "/" + project,
		Branch:             bootstrap.TrunkBranch,
		ImageName:          BuildName(project, svc.Name),
		AutoIncrement:      false,
		IncludeVersionTags: false,
		IncludeCommitTag:   true,
		ImageRegistry: []komodo.ImageRegistry{{
			Domain:       r.Internal.Registry,
			Account:      bootstrap.GiteaAdminUser,
			Organization: r.Org,
		}},
		BuildArgs: "SERVICE=" + svc.Name,
	}
	if svc.IsWeb() && svc.Frontend != nil {
		cfg.BuildArgs += "\nBUILD_COMMAND=" + svc.Frontend.BuildCommand + "\nOUTPUT_DIR=" + svc.Frontend.OutputDir
	}
	if svc.Runtime == "docker" {
		dockerfile := path.Clean(strings.TrimPrefix(svc.Dockerfile, "./"))
		cfg.BuildPath = path.Dir(dockerfile)
		cfg.DockerfilePath = path.Base(dockerfile)
		return cfg, nil
	}
	contents, err := platformassets.Dockerfiles.ReadFile("dockerfiles/" + svc.Runtime + ".Dockerfile")
	if err != nil {
		return komodo.BuildConfig{}, fmt.Errorf("no urbex Dockerfile for runtime %q", svc.Runtime)
	}
	cfg.BuildPath = "."
	cfg.DockerfilePath = GeneratedDockerfile
	// Komodo only uses UI-defined Dockerfile contents for repo-less
	// builds, so write ours next to the cloned sources instead.
	cfg.PreBuild = komodo.SystemCommand{
		Path:      ".",
		Command:   "cat > " + GeneratedDockerfile + " <<'URBEX_DOCKERFILE'\n" + string(contents) + "URBEX_DOCKERFILE\n",
		ShellMode: true,
	}
	return cfg, nil
}

// ComposeWrapper is the command every Stack runs `docker compose up`
// through: a script on the LXC (see the periphery Ansible role) that
// decrypts the service's secrets, if it has any, for the duration of the
// command.
const ComposeWrapper = bootstrap.ComposeWrapper

// stackConfig is the Komodo Stack of one service in one environment: the
// compose project in the service's folder of the GitOps repo, on the
// service's LXC. The version file is an --env-file (it sets the image
// tag); it, the configuration, and the secrets are tracked, so a change
// to any of them redeploys the Stack.
//
// Nothing here depends on the folder's contents, on purpose: the Stack
// is declared once and everything after that is a commit. Updating a
// Stack right before pushing would also defeat the deploy - Komodo
// caches the repo for a few seconds after looking at it.
func (r *Rollout) stackConfig(name string, t Target, svc manifest.Service) komodo.StackConfig {
	if svc.IsWeb() {
		return r.webStackConfig(t, svc)
	}
	configFiles := []komodo.ConfigFile{
		{Path: envrepo.ConfigFile, Requires: "Redeploy"},
		{Path: envrepo.SecretsFile, Requires: "Redeploy"},
	}
	var ignore []string
	if links := t.Links[svc.Name]; links != nil {
		configFiles = append(configFiles,
			komodo.ConfigFile{Path: envrepo.GeneratedFile, Requires: "Redeploy"},
			komodo.ConfigFile{Path: envrepo.GeneratedSecretsFile, Requires: "Redeploy"})
		if len(links.Waits) > 0 {
			// The wait exits once what the application uses answers: left
			// out, Komodo would show the Stack as unhealthy.
			ignore = []string{"wait"}
		}
	}
	return komodo.StackConfig{
		IgnoreServices:     ignore,
		ServerID:           name, // the LXC's Periphery registers under its hostname
		GitProvider:        r.Internal.Registry,
		GitHTTPS:           false,
		GitAccount:         bootstrap.GiteaAdminUser,
		Repo:               r.Org + "/" + bootstrap.GitOpsRepoName,
		Branch:             bootstrap.TrunkBranch,
		RunDirectory:       envrepo.ServiceDir(t.Env, t.Project, svc.Name),
		AdditionalEnvFiles: []komodo.EnvFile{{Path: envrepo.VersionFile, Track: true}},
		ConfigFiles:        configFiles,
		RegistryProvider:   r.Internal.Registry,
		RegistryAccount:    bootstrap.GiteaAdminUser,
		ComposeCmdWrapper:  ComposeWrapper,
	}
}

// GitCLI implements Git with the git binary (see internal/gitsync).
type GitCLI struct{}

func (GitCLI) Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	return gitsync.Push(ctx, dir, remoteURL, token, refspecs...)
}

// WorkerName is the Cloudflare Worker a project's web frontend is
// deployed to in an environment.
func WorkerName(project, env string) string {
	return "urbex-" + project + "-" + manifest.WebService + "-" + env
}

// Komodo variables the web frontends' Stacks deploy to Cloudflare with
// (set by urbex bootstrap when the platform has Cloudflare).
const (
	CloudflareTokenVariable   = "URBEX_CLOUDFLARE_TOKEN"
	CloudflareAccountVariable = "URBEX_CLOUDFLARE_ACCOUNT_ID"
)

// webStackConfig is the Stack of a web frontend in an environment: on the
// builder, it runs the frontend's image, which uploads its assets to the
// environment's Worker and stays up - healthy once that worked.
func (r *Rollout) webStackConfig(t Target, svc manifest.Service) komodo.StackConfig {
	env := strings.Join([]string{
		"WORKER_NAME=" + WorkerName(t.Project, t.Env),
		"WORKER_HOSTNAME=" + t.Hostnames[svc.Name],
		"CLOUDFLARE_API_TOKEN=[[" + CloudflareTokenVariable + "]]",
		"CLOUDFLARE_ACCOUNT_ID=[[" + CloudflareAccountVariable + "]]",
	}, "\n")
	return komodo.StackConfig{
		ServerID:           bootstrap.KomodoServerName,
		GitProvider:        r.Internal.Registry,
		GitHTTPS:           false,
		GitAccount:         bootstrap.GiteaAdminUser,
		Repo:               r.Org + "/" + bootstrap.GitOpsRepoName,
		Branch:             bootstrap.TrunkBranch,
		RunDirectory:       envrepo.ServiceDir(t.Env, t.Project, svc.Name),
		AdditionalEnvFiles: []komodo.EnvFile{{Path: envrepo.VersionFile, Track: true}},
		RegistryProvider:   r.Internal.Registry,
		RegistryAccount:    bootstrap.GiteaAdminUser,
		Environment:        env,
	}
}
