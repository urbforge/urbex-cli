// Package rollout declares, in Komodo and Gitea, everything needed for a
// project environment to build and deploy itself whenever its branch
// changes: per service a Komodo Build (from the app repo on Gitea, pushed
// to Gitea's registry) and a Deployment (on the service's LXC), a
// Procedure running them all, and a Gitea webhook that triggers the
// Procedure on every push to the environment's branch. See
// docs/decisions/0018-image-build-komodo-gitea-registry.md and
// docs/decisions/0019-branch-environments-komodo-rollouts.md in
// urbforge/urbex.
package rollout

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	platformassets "github.com/urbforge/urbex-cli/platform"
)

// GeneratedDockerfile is where a Build's pre-build step writes the Urbex
// Dockerfile for language runtimes, inside Komodo's clone of the repo.
const GeneratedDockerfile = "Dockerfile.urbex"

// branches maps each environment to the app-repo branch it deploys.
var branches = map[string]string{"staging": "staging", "prod": "main"}

// Branch returns the app-repo branch whose head env runs.
func Branch(env string) string { return branches[env] }

// ResourceName names a service's LXC, Komodo Server, Build, and
// Deployment in one environment, e.g. "my-app-api-staging".
func ResourceName(project, service, env string) string {
	return project + "-" + service + "-" + env
}

// ProcedureName names an environment's build-and-deploy Procedure.
func ProcedureName(project, env string) string { return project + "-" + env }

// Gitea is the subset of the Gitea client a rollout uses.
type Gitea interface {
	EnsureRepo(ctx context.Context, owner, name string) error
	BranchExists(ctx context.Context, owner, repo, branch string) (bool, error)
	EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, branch string) error
	DeleteWebhook(ctx context.Context, owner, repo, targetURL string) error
}

// Komodo is the subset of the Komodo client a rollout uses.
type Komodo interface {
	WaitServers(ctx context.Context, names []string, timeout time.Duration) (rekeyed []string, err error)
	EnsureBuild(ctx context.Context, name string, cfg komodo.BuildConfig) error
	EnsureDeployment(ctx context.Context, name string, cfg komodo.DeploymentConfig) error
	EnsureProcedure(ctx context.Context, name string, cfg komodo.ProcedureConfig) error
	RunProcedure(ctx context.Context, name string) (*komodo.Update, error)
	WaitUpdate(ctx context.Context, id string, interval time.Duration) (*komodo.Update, error)
	ExplainFailure(ctx context.Context, u *komodo.Update) string
	ListDeployments(ctx context.Context) (map[string]komodo.DeploymentStatus, error)
	DeleteResource(ctx context.Context, kind, name string) error
}

// Git is the subset of git operations a rollout uses on the app repo.
type Git interface {
	Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error
}

// Rollout manages project environments through Gitea and Komodo.
type Rollout struct {
	Gitea     Gitea
	Komodo    Komodo
	Git       Git
	Endpoints bootstrap.Endpoints
	Org       string
	Token     string // Gitea token with write:repository
	// WebhookSecret is Komodo's webhook secret, which Gitea signs with.
	WebhookSecret string
	// RepoDir is the local working copy of the app repo, used only to
	// create an environment's branch on Gitea when it doesn't exist yet.
	RepoDir string
	Out     io.Writer
	// PollInterval is how often a running procedure is polled; 0 means 3s.
	PollInterval time.Duration
	// ServerTimeout is how long to wait for the LXCs' Periphery agents to
	// connect to Komodo; 0 means 3 minutes.
	ServerTimeout time.Duration
}

// Target is one environment of one project.
type Target struct {
	Project  string
	Env      string
	Services []manifest.Service
}

// Resources are the Komodo resources of a Target, as urbex declared
// them. They are also written to the GitOps repo as the record of what
// Komodo was asked to run.
type Resources struct {
	Procedure     string                             `json:"procedure"`
	ProcedureSpec komodo.ProcedureConfig             `json:"procedureConfig"`
	Branch        string                             `json:"branch"`
	Webhook       string                             `json:"webhook"`
	Builds        map[string]komodo.BuildConfig      `json:"builds"`
	Deployments   map[string]komodo.DeploymentConfig `json:"deployments"`
}

// Deployed is the state of one service after a run.
type Deployed struct {
	Service string
	Image   string
	State   string
}

// Setup makes Gitea and Komodo ready to build and deploy t on every push
// to its branch: the app repo and the branch on Gitea, the Builds,
// Deployments, and Procedure on Komodo, and the webhook tying them
// together. It is idempotent and re-syncs everything from t.
func (r *Rollout) Setup(ctx context.Context, t Target) (*Resources, error) {
	branch := Branch(t.Env)
	if branch == "" {
		return nil, fmt.Errorf("no branch defined for environment %q", t.Env)
	}
	if err := r.Gitea.EnsureRepo(ctx, r.Org, t.Project); err != nil {
		return nil, err
	}
	if err := r.ensureBranch(ctx, t.Project, branch); err != nil {
		return nil, err
	}

	var names []string
	for _, svc := range t.Services {
		names = append(names, ResourceName(t.Project, svc.Name, t.Env))
	}
	timeout := r.ServerTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	rekeyed, err := r.Komodo.WaitServers(ctx, names, timeout)
	for _, name := range rekeyed {
		fmt.Fprintf(r.Out, "Komodo server %s reconnected with a new key (its LXC was recreated): accepted.\n", name)
	}
	if err != nil {
		return nil, fmt.Errorf("%w - is Periphery running on those LXCs? ('urbex apply %s' installs it)", err, t.Env)
	}

	procedure := ProcedureName(t.Project, t.Env)
	res := &Resources{
		Procedure:   procedure,
		Branch:      branch,
		Webhook:     r.Endpoints.KomodoURL + "/listener/github/procedure/" + procedure + "/" + branch,
		Builds:      map[string]komodo.BuildConfig{},
		Deployments: map[string]komodo.DeploymentConfig{},
	}
	for i, svc := range t.Services {
		build, err := r.buildConfig(t, svc, branch)
		if err != nil {
			return nil, err
		}
		if err := r.Komodo.EnsureBuild(ctx, names[i], build); err != nil {
			return nil, err
		}
		deployment := deploymentConfig(names[i], svc, t.Env)
		if err := r.Komodo.EnsureDeployment(ctx, names[i], deployment); err != nil {
			return nil, err
		}
		res.Builds[names[i]] = build
		res.Deployments[names[i]] = deployment
	}

	// A Procedure rather than Komodo's redeploy-on-build: that one skips
	// containers that aren't running, so a crashed service would never
	// pick up its fix.
	res.ProcedureSpec = komodo.ProcedureConfig{
		WebhookEnabled: true,
		Stages: []komodo.Stage{
			komodo.NewStage("Build", "RunBuild", "build", names),
			komodo.NewStage("Deploy", "Deploy", "deployment", names),
		},
	}
	if err := r.Komodo.EnsureProcedure(ctx, procedure, res.ProcedureSpec); err != nil {
		return nil, err
	}
	if err := r.Gitea.EnsureWebhook(ctx, r.Org, t.Project, res.Webhook, r.WebhookSecret, branch); err != nil {
		return nil, err
	}
	return res, nil
}

// ensureBranch creates branch on Gitea from the local HEAD if Gitea
// doesn't have it yet, so a new project or environment has something to
// build. An existing branch is never touched: it moves by merging.
func (r *Rollout) ensureBranch(ctx context.Context, project, branch string) error {
	exists, err := r.Gitea.BranchExists(ctx, r.Org, project, branch)
	if err != nil || exists {
		return err
	}
	if r.RepoDir == "" {
		return fmt.Errorf("branch %s doesn't exist in %s/%s on gitea - push it first", branch, r.Org, project)
	}
	url := r.Endpoints.RepoURL(r.Org, project)
	if err := r.Git.Push(ctx, r.RepoDir, url, r.Token, "HEAD:refs/heads/"+branch); err != nil {
		return fmt.Errorf("creating branch %s on gitea from %s: %w", branch, r.RepoDir, err)
	}
	fmt.Fprintf(r.Out, "Created branch %s of %s/%s on Gitea from the local HEAD.\n", branch, r.Org, project)
	return nil
}

// Run builds and deploys t now, the same way a push to its branch does,
// and returns what each service is running afterwards.
func (r *Rollout) Run(ctx context.Context, t Target) ([]Deployed, error) {
	procedure := ProcedureName(t.Project, t.Env)
	fmt.Fprintf(r.Out, "Building and deploying %s from branch %s on Komodo...\n", procedure, Branch(t.Env))
	started, err := r.Komodo.RunProcedure(ctx, procedure)
	if err != nil {
		return nil, err
	}
	interval := r.PollInterval
	if interval == 0 {
		interval = 3 * time.Second
	}
	done, err := r.Komodo.WaitUpdate(ctx, started.ID.OID, interval)
	if err != nil {
		return nil, err
	}
	if !done.Success {
		return nil, fmt.Errorf("komodo procedure %s failed - %s", procedure, r.Komodo.ExplainFailure(ctx, done))
	}
	return r.Status(ctx, t)
}

// Status returns what Komodo reports each service of t is running.
func (r *Rollout) Status(ctx context.Context, t Target) ([]Deployed, error) {
	statuses, err := r.Komodo.ListDeployments(ctx)
	if err != nil {
		return nil, err
	}
	deployed := make([]Deployed, 0, len(t.Services))
	for _, svc := range t.Services {
		st, ok := statuses[ResourceName(t.Project, svc.Name, t.Env)]
		if !ok {
			st.State = "not_declared"
		}
		deployed = append(deployed, Deployed{Service: svc.Name, Image: st.Image, State: st.State})
	}
	return deployed, nil
}

// Remove deletes t's webhook and its Komodo Procedure, Deployments, and
// Builds. Call it before destroying the LXCs, while their agents can
// still take the containers down, and RemoveServers after.
func (r *Rollout) Remove(ctx context.Context, t Target) error {
	procedure := ProcedureName(t.Project, t.Env)
	webhook := r.Endpoints.KomodoURL + "/listener/github/procedure/" + procedure + "/" + Branch(t.Env)
	if err := r.Gitea.DeleteWebhook(ctx, r.Org, t.Project, webhook); err != nil {
		return err
	}
	if err := r.Komodo.DeleteResource(ctx, "Procedure", procedure); err != nil {
		return err
	}
	// Deployments reference Builds, so they go first.
	for _, kind := range []string{"Deployment", "Build"} {
		for _, svc := range t.Services {
			if err := r.Komodo.DeleteResource(ctx, kind, ResourceName(t.Project, svc.Name, t.Env)); err != nil {
				return err
			}
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

// buildConfig is the Komodo Build of one service in one environment:
// clone the environment's branch from Gitea, build with the service's own
// Dockerfile (runtime docker) or Urbex's per-runtime one, and push to
// Gitea's registry as <project>-<service>:<version>-<env>.
func (r *Rollout) buildConfig(t Target, svc manifest.Service, branch string) (komodo.BuildConfig, error) {
	cfg := komodo.BuildConfig{
		BuilderID:          bootstrap.KomodoServerName,
		GitProvider:        r.Endpoints.Registry, // Gitea's host:port serves both git and the registry
		GitHTTPS:           false,
		GitAccount:         bootstrap.GiteaAdminUser,
		Repo:               r.Org + "/" + t.Project,
		Branch:             branch,
		ImageName:          t.Project + "-" + svc.Name,
		ImageTag:           t.Env,
		AutoIncrement:      true,
		IncludeVersionTags: true,
		IncludeCommitTag:   true,
		ImageRegistry: []komodo.ImageRegistry{{
			Domain:       r.Endpoints.Registry,
			Account:      bootstrap.GiteaAdminUser,
			Organization: r.Org,
		}},
		BuildArgs: "SERVICE=" + svc.Name,
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

// deploymentConfig is the Komodo Deployment of one service in one
// environment: the latest image of its Build, on its own LXC, with the
// port, environment, and healthcheck from the manifest.
func deploymentConfig(name string, svc manifest.Service, env string) komodo.DeploymentConfig {
	environment := []string{fmt.Sprintf("PORT=%d", svc.Port)}
	vars := svc.EnvFor(env)
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		environment = append(environment, k+"="+vars[k])
	}

	cfg := komodo.DeploymentConfig{
		ServerID:    name, // the LXC's Periphery registers under its hostname
		Image:       komodo.BuildImage(name),
		Network:     "bridge",
		Restart:     "unless-stopped",
		Ports:       fmt.Sprintf("%d:%d", svc.Port, svc.Port),
		Environment: strings.Join(environment, "\n"),
		ExtraArgs:   []string{},
	}
	if svc.Healthcheck != "" {
		// Extra args land verbatim in the `docker run` command line, so
		// the health command has to carry its own quotes.
		cfg.ExtraArgs = []string{
			fmt.Sprintf(`--health-cmd="wget -q --spider http://localhost:%d%s"`, svc.Port, svc.Healthcheck),
			"--health-interval=30s",
			"--health-timeout=5s",
			"--health-retries=3",
		}
	}
	return cfg
}

// GitCLI implements Git with the git binary (see internal/gitsync).
type GitCLI struct{}

func (GitCLI) Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	return gitsync.Push(ctx, dir, remoteURL, token, refspecs...)
}
