package rollout

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type fakeGitea struct {
	repos    []string
	branches map[string]bool
	hooks    map[string]string // url -> "secret|branch"
}

func (f *fakeGitea) EnsureRepo(ctx context.Context, owner, name string) error {
	f.repos = append(f.repos, owner+"/"+name)
	return nil
}
func (f *fakeGitea) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	return f.branches[branch], nil
}
func (f *fakeGitea) EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, branch string) error {
	if f.hooks == nil {
		f.hooks = map[string]string{}
	}
	f.hooks[targetURL] = secret + "|" + branch
	return nil
}
func (f *fakeGitea) DeleteWebhook(ctx context.Context, owner, repo, targetURL string) error {
	delete(f.hooks, targetURL)
	return nil
}

type fakeKomodo struct {
	waited      []string
	waitErr     error
	builds      map[string]komodo.BuildConfig
	deployments map[string]komodo.DeploymentConfig
	procedures  map[string]komodo.ProcedureConfig
	runs        []string
	fail        bool
	deleted     []string
}

func (f *fakeKomodo) WaitServers(ctx context.Context, names []string, timeout time.Duration) ([]string, error) {
	f.waited = names
	return nil, f.waitErr
}
func (f *fakeKomodo) EnsureBuild(ctx context.Context, name string, cfg komodo.BuildConfig) error {
	if f.builds == nil {
		f.builds = map[string]komodo.BuildConfig{}
	}
	f.builds[name] = cfg
	return nil
}
func (f *fakeKomodo) EnsureDeployment(ctx context.Context, name string, cfg komodo.DeploymentConfig) error {
	if f.deployments == nil {
		f.deployments = map[string]komodo.DeploymentConfig{}
	}
	f.deployments[name] = cfg
	return nil
}
func (f *fakeKomodo) EnsureProcedure(ctx context.Context, name string, cfg komodo.ProcedureConfig) error {
	if f.procedures == nil {
		f.procedures = map[string]komodo.ProcedureConfig{}
	}
	f.procedures[name] = cfg
	return nil
}
func (f *fakeKomodo) RunProcedure(ctx context.Context, name string) (*komodo.Update, error) {
	f.runs = append(f.runs, name)
	u := &komodo.Update{Status: "InProgress"}
	u.ID.OID = "u1"
	return u, nil
}
func (f *fakeKomodo) WaitUpdate(ctx context.Context, id string, interval time.Duration) (*komodo.Update, error) {
	return &komodo.Update{Status: "Complete", Success: !f.fail}, nil
}
func (f *fakeKomodo) ExplainFailure(ctx context.Context, u *komodo.Update) string {
	return "stage \"Docker Build\" failed:\nno main package"
}
func (f *fakeKomodo) ListDeployments(ctx context.Context) (map[string]komodo.DeploymentStatus, error) {
	return map[string]komodo.DeploymentStatus{
		"my-app-api-staging": {State: "running", Image: "10.0.0.200:3000/urbex/my-app-api:0.0.3-staging"},
	}, nil
}
func (f *fakeKomodo) DeleteResource(ctx context.Context, kind, name string) error {
	f.deleted = append(f.deleted, kind+" "+name)
	return nil
}

type fakeGit struct{ pushes []string }

func (f *fakeGit) Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	f.pushes = append(f.pushes, remoteURL+" "+strings.Join(refspecs, " "))
	return nil
}

func newRollout(g *fakeGitea, k *fakeKomodo, git *fakeGit) *Rollout {
	return &Rollout{
		Gitea:  g,
		Komodo: k,
		Git:    git,
		Endpoints: bootstrap.Endpoints{
			GiteaURL:  "http://10.0.0.200:3000",
			Registry:  "10.0.0.200:3000",
			KomodoURL: "http://10.0.0.201:9120",
		},
		Org:           "urbex",
		Token:         "tok",
		WebhookSecret: "hook-secret",
		RepoDir:       "/app",
		Out:           &bytes.Buffer{},
		PollInterval:  time.Millisecond,
	}
}

func stagingTarget() Target {
	return Target{
		Project: "my-app",
		Env:     "staging",
		Services: []manifest.Service{
			{Name: "api", Runtime: "go", Port: 8080, Healthcheck: "/healthz",
				Env: map[string]map[string]string{"staging": {"LOG_LEVEL": "debug", "A": "1"}, "prod": {"LOG_LEVEL": "info"}}},
			{Name: "worker", Runtime: "docker", Dockerfile: "./worker/Dockerfile", Port: 9000},
		},
	}
}

func TestSetup_DeclaresBuildsDeploymentsProcedureAndWebhook(t *testing.T) {
	g, k, git := &fakeGitea{branches: map[string]bool{"staging": true}}, &fakeKomodo{}, &fakeGit{}

	res, err := newRollout(g, k, git).Setup(context.Background(), stagingTarget())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if strings.Join(g.repos, ",") != "urbex/my-app" {
		t.Errorf("expected the app repo to be ensured on gitea, got %v", g.repos)
	}
	if len(git.pushes) != 0 {
		t.Errorf("an existing branch must never be pushed to, got %v", git.pushes)
	}
	if strings.Join(k.waited, ",") != "my-app-api-staging,my-app-worker-staging" {
		t.Errorf("expected to wait for both servers, got %v", k.waited)
	}

	api := k.builds["my-app-api-staging"]
	if api.Branch != "staging" || api.Repo != "urbex/my-app" || api.ImageName != "my-app-api" || api.ImageTag != "staging" {
		t.Errorf("unexpected api build %+v", api)
	}
	if !api.AutoIncrement || !api.IncludeVersionTags || !api.IncludeCommitTag || api.IncludeLatestTag || api.WebhookEnabled {
		t.Errorf("unexpected api build tagging/webhook flags %+v", api)
	}
	if api.DockerfilePath != GeneratedDockerfile || !strings.Contains(api.PreBuild.Command, "golang") {
		t.Errorf("expected the go runtime Dockerfile written by pre_build, got %+v", api.PreBuild)
	}
	worker := k.builds["my-app-worker-staging"]
	if worker.BuildPath != "worker" || worker.DockerfilePath != "Dockerfile" || worker.PreBuild.Command != "" {
		t.Errorf("expected the service's own Dockerfile with its directory as context, got %+v", worker)
	}

	dep := k.deployments["my-app-api-staging"]
	if dep.ServerID != "my-app-api-staging" || dep.Image.Params.BuildID != "my-app-api-staging" || dep.Image.Type != "Build" {
		t.Errorf("unexpected deployment target %+v", dep)
	}
	if dep.Ports != "8080:8080" || dep.Environment != "PORT=8080\nA=1\nLOG_LEVEL=debug" || dep.RedeployOnBuild {
		t.Errorf("unexpected deployment runtime config %+v", dep)
	}
	if len(dep.ExtraArgs) != 4 || dep.ExtraArgs[0] != `--health-cmd="wget -q --spider http://localhost:8080/healthz"` {
		t.Errorf("unexpected healthcheck args %v", dep.ExtraArgs)
	}
	if args := k.deployments["my-app-worker-staging"].ExtraArgs; len(args) != 0 {
		t.Errorf("expected no healthcheck args without a healthcheck path, got %v", args)
	}

	proc := k.procedures["my-app-staging"]
	if !proc.WebhookEnabled || len(proc.Stages) != 2 {
		t.Fatalf("unexpected procedure %+v", proc)
	}
	if proc.Stages[0].Executions[1].Execution.Type != "RunBuild" || proc.Stages[1].Executions[0].Execution.Params["deployment"] != "my-app-api-staging" {
		t.Errorf("expected builds then deployments, got %+v", proc.Stages)
	}

	const hook = "http://10.0.0.201:9120/listener/github/procedure/my-app-staging/staging"
	if g.hooks[hook] != "hook-secret|staging" || res.Webhook != hook {
		t.Errorf("unexpected webhooks %v (resources say %q)", g.hooks, res.Webhook)
	}
	if len(res.Builds) != 2 || len(res.Deployments) != 2 || res.Procedure != "my-app-staging" {
		t.Errorf("unexpected resources record %+v", res)
	}
}

func TestSetup_CreatesMissingBranchFromLocalHead(t *testing.T) {
	g, k, git := &fakeGitea{branches: map[string]bool{}}, &fakeKomodo{}, &fakeGit{}
	target := stagingTarget()
	target.Env = "prod"

	if _, err := newRollout(g, k, git).Setup(context.Background(), target); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(git.pushes) != 1 || git.pushes[0] != "http://10.0.0.200:3000/urbex/my-app.git HEAD:refs/heads/main" {
		t.Errorf("expected prod's missing main branch to be created from HEAD, got %v", git.pushes)
	}
	if k.builds["my-app-api-prod"].Branch != "main" || k.builds["my-app-api-prod"].ImageTag != "prod" {
		t.Errorf("unexpected prod build %+v", k.builds["my-app-api-prod"])
	}
	if k.deployments["my-app-api-prod"].Environment != "PORT=8080\nLOG_LEVEL=info" {
		t.Errorf("expected prod env vars, got %q", k.deployments["my-app-api-prod"].Environment)
	}
}

func TestSetup_FailsWhenServersNeverConnect(t *testing.T) {
	k := &fakeKomodo{waitErr: errors.New("komodo servers not connected: [my-app-api-staging]")}
	_, err := newRollout(&fakeGitea{branches: map[string]bool{"staging": true}}, k, &fakeGit{}).Setup(context.Background(), stagingTarget())
	if err == nil || !strings.Contains(err.Error(), "Periphery") {
		t.Errorf("expected a hint about Periphery, got %v", err)
	}
	if len(k.builds) != 0 {
		t.Errorf("nothing should be declared before the servers connect, got %v", k.builds)
	}
}

func TestRun_ReturnsWhatKomodoDeployed(t *testing.T) {
	k := &fakeKomodo{}
	deployed, err := newRollout(&fakeGitea{}, k, &fakeGit{}).Run(context.Background(), stagingTarget())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(k.runs, ",") != "my-app-staging" {
		t.Errorf("expected the environment's procedure to run, got %v", k.runs)
	}
	if deployed[0].Service != "api" || deployed[0].State != "running" || !strings.HasSuffix(deployed[0].Image, "my-app-api:0.0.3-staging") {
		t.Errorf("unexpected api status %+v", deployed[0])
	}
	if deployed[1].State != "not_declared" {
		t.Errorf("expected a service komodo doesn't know to be reported as such, got %+v", deployed[1])
	}
}

func TestRun_FailureSurfacesKomodoLogs(t *testing.T) {
	_, err := newRollout(&fakeGitea{}, &fakeKomodo{fail: true}, &fakeGit{}).Run(context.Background(), stagingTarget())
	if err == nil || !strings.Contains(err.Error(), "no main package") {
		t.Errorf("expected the failed stage's output in the error, got %v", err)
	}
}

func TestRemove_DeletesWebhookAndResourcesInDependencyOrder(t *testing.T) {
	const hook = "http://10.0.0.201:9120/listener/github/procedure/my-app-staging/staging"
	g := &fakeGitea{hooks: map[string]string{hook: "x"}}
	k := &fakeKomodo{}
	r := newRollout(g, k, &fakeGit{})
	if err := r.Remove(context.Background(), stagingTarget()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(g.hooks) != 0 {
		t.Errorf("expected the webhook to be deleted, got %v", g.hooks)
	}
	want := "Procedure my-app-staging,Deployment my-app-api-staging,Deployment my-app-worker-staging," +
		"Build my-app-api-staging,Build my-app-worker-staging"
	if strings.Join(k.deleted, ",") != want {
		t.Errorf("unexpected deletions (servers must outlive Remove):\n got %v\nwant %s", k.deleted, want)
	}

	k.deleted = nil
	if err := r.RemoveServers(context.Background(), stagingTarget()); err != nil {
		t.Fatalf("RemoveServers: %v", err)
	}
	if strings.Join(k.deleted, ",") != "Server my-app-api-staging,Server my-app-worker-staging" {
		t.Errorf("unexpected server deletions %v", k.deleted)
	}
}

func TestEveryRuntimeHasADockerfile(t *testing.T) {
	r := newRollout(&fakeGitea{}, &fakeKomodo{}, &fakeGit{})
	for _, runtime := range []string{"go", "java", "python"} {
		if _, err := r.buildConfig(Target{Project: "p", Env: "staging"}, manifest.Service{Name: "s", Runtime: runtime}, "staging"); err != nil {
			t.Errorf("runtime %s: %v", runtime, err)
		}
	}
}
