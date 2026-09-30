package rollout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/semver"
)

type fakeGitea struct {
	repos    []string
	branches map[string]bool
	hooks    map[string]string // url -> "secret|filter"
	tags     []string
	images   map[string]bool // "name:tag"
	// imagesAfter makes images appear after that many lookups, as a
	// build finishing would.
	imagesAfter int
	lookups     int
}

func (f *fakeGitea) EnsureRepo(ctx context.Context, owner, name string) error {
	f.repos = append(f.repos, owner+"/"+name)
	return nil
}
func (f *fakeGitea) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	return f.branches[branch], nil
}
func (f *fakeGitea) EnsureWebhook(ctx context.Context, owner, repo, targetURL, secret, filter string) error {
	if f.hooks == nil {
		f.hooks = map[string]string{}
	}
	f.hooks[targetURL] = secret + "|" + filter
	return nil
}
func (f *fakeGitea) DeleteWebhook(ctx context.Context, owner, repo, targetURL string) error {
	delete(f.hooks, targetURL)
	return nil
}
func (f *fakeGitea) ListTags(ctx context.Context, owner, repo string) ([]string, error) {
	return f.tags, nil
}
func (f *fakeGitea) CreateTag(ctx context.Context, owner, repo, tag, target, message string) error {
	f.tags = append(f.tags, tag+"@"+target)
	return nil
}
func (f *fakeGitea) ContainerImageExists(ctx context.Context, owner, name, tag string) (bool, error) {
	f.lookups++
	if f.imagesAfter > 0 && f.lookups > f.imagesAfter {
		return true, nil
	}
	return f.images[name+":"+tag], nil
}

type fakeKomodo struct {
	waited     []string
	waitErr    error
	builds     map[string]komodo.BuildConfig
	stacks     map[string]komodo.StackConfig
	status     map[string]komodo.StackStatus
	deployed   []string
	deployFail bool
	destroyed  []string
	deleted    []string
	reconciles int
	// onReconcile lets a test change status when Komodo is nudged.
	onReconcile func()
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
func (f *fakeKomodo) EnsureStack(ctx context.Context, name string, cfg komodo.StackConfig) error {
	if f.stacks == nil {
		f.stacks = map[string]komodo.StackConfig{}
	}
	f.stacks[name] = cfg
	return nil
}
func (f *fakeKomodo) ListStacks(ctx context.Context) (map[string]komodo.StackStatus, error) {
	return f.status, nil
}
func (f *fakeKomodo) DeployStack(ctx context.Context, name string) (*komodo.Update, error) {
	f.deployed = append(f.deployed, name)
	return &komodo.Update{Status: "Complete", Success: !f.deployFail}, nil
}
func (f *fakeKomodo) ExplainFailure(ctx context.Context, u *komodo.Update) string {
	return "stage \"Compose Up\" failed:\nmanifest unknown"
}
func (f *fakeKomodo) RunProcedure(ctx context.Context, name string) error {
	f.reconciles++
	if f.onReconcile != nil {
		f.onReconcile()
	}
	return nil
}
func (f *fakeKomodo) DestroyStack(ctx context.Context, name string) error {
	f.destroyed = append(f.destroyed, name)
	return nil
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
		CacheWindow:   time.Millisecond,
		NudgeAfter:    20 * time.Millisecond,
		Timeout:       300 * time.Millisecond,
	}
}

func services() []manifest.Service {
	return []manifest.Service{
		{Name: "api", Runtime: "go", Port: 8080, Healthcheck: "/healthz",
			Env: map[string]map[string]string{"staging": {"LOG_LEVEL": "debug"}}},
		{Name: "worker", Runtime: "docker", Dockerfile: "./worker/Dockerfile", Port: 9000},
	}
}

func stagingTarget() Target {
	return Target{Project: "my-app", Env: "staging", Services: services()}
}

func TestSetupProject_DeclaresBuildsAndReleaseWebhook(t *testing.T) {
	g, k, git := &fakeGitea{branches: map[string]bool{"main": true}}, &fakeKomodo{}, &fakeGit{}

	if err := newRollout(g, k, git).SetupProject(context.Background(), "my-app", services()); err != nil {
		t.Fatalf("SetupProject: %v", err)
	}

	if strings.Join(g.repos, ",") != "urbex/my-app" {
		t.Errorf("expected the project repo to be ensured on gitea, got %v", g.repos)
	}
	if len(git.pushes) != 0 {
		t.Errorf("an existing main branch must never be pushed to, got %v", git.pushes)
	}

	api := k.builds["my-app-api"]
	if api.Branch != "main" || api.Repo != "urbex/my-app" || api.ImageName != "my-app-api" {
		t.Errorf("unexpected api build %+v", api)
	}
	// The release Action sets version and commit for each tag: the Build
	// itself must neither invent versions nor react to pushes.
	if api.AutoIncrement || !api.IncludeVersionTags || !api.IncludeCommitTag || api.IncludeLatestTag || api.WebhookEnabled {
		t.Errorf("unexpected api build tagging/webhook flags %+v", api)
	}
	if api.DockerfilePath != GeneratedDockerfile || !strings.Contains(api.PreBuild.Command, "golang") {
		t.Errorf("expected the go runtime Dockerfile written by pre_build, got %+v", api.PreBuild)
	}
	worker := k.builds["my-app-worker"]
	if worker.BuildPath != "worker" || worker.DockerfilePath != "Dockerfile" || worker.PreBuild.Command != "" {
		t.Errorf("expected the service's own Dockerfile with its directory as context, got %+v", worker)
	}
	if len(k.builds) != 2 {
		t.Errorf("expected one build per service, independent of environments, got %d", len(k.builds))
	}

	const hook = "http://10.0.0.201:9120/listener/github/action/urbex-release/__ANY__"
	if g.hooks[hook] != "hook-secret|*" {
		t.Errorf("expected a release webhook for every ref (tags included), got %v", g.hooks)
	}
}

func TestSetupProject_CreatesMainFromLocalHead(t *testing.T) {
	g, git := &fakeGitea{branches: map[string]bool{}}, &fakeGit{}
	if err := newRollout(g, &fakeKomodo{}, git).SetupProject(context.Background(), "my-app", services()); err != nil {
		t.Fatalf("SetupProject: %v", err)
	}
	if len(git.pushes) != 1 || git.pushes[0] != "http://10.0.0.200:3000/urbex/my-app.git HEAD:refs/heads/main" {
		t.Errorf("expected main to be created from HEAD, got %v", git.pushes)
	}
}

func TestSetupEnv_WritesFoldersAndDeclaresStacks(t *testing.T) {
	k := &fakeKomodo{}
	repo := envrepo.Repo{Path: t.TempDir()}

	if err := newRollout(&fakeGitea{}, k, &fakeGit{}).SetupEnv(context.Background(), stagingTarget(), repo); err != nil {
		t.Fatalf("SetupEnv: %v", err)
	}

	if strings.Join(k.waited, ",") != "my-app-api-staging,my-app-worker-staging" {
		t.Errorf("expected to wait for both servers, got %v", k.waited)
	}
	compose, err := os.ReadFile(filepath.Join(repo.Path, "environments/staging/my-app/api/compose.yaml"))
	if err != nil || !strings.Contains(string(compose), "image: 10.0.0.200:3000/urbex/my-app-api:${VERSION}") {
		t.Errorf("expected the api folder with its compose file (%v):\n%s", err, compose)
	}

	stack := k.stacks["my-app-api-staging"]
	if stack.ServerID != "my-app-api-staging" || stack.Repo != "urbex/gitops" || stack.Branch != "main" ||
		stack.RunDirectory != "environments/staging/my-app/api" {
		t.Errorf("unexpected stack source %+v", stack)
	}
	if len(stack.AdditionalEnvFiles) != 1 || stack.AdditionalEnvFiles[0] != (komodo.EnvFile{Path: "version.env", Track: true}) {
		t.Errorf("expected version.env as a tracked --env-file, got %+v", stack.AdditionalEnvFiles)
	}
	if len(stack.ConfigFiles) != 2 || stack.ConfigFiles[0] != (komodo.ConfigFile{Path: "config.env", Requires: "Redeploy"}) ||
		stack.ConfigFiles[1] != (komodo.ConfigFile{Path: "secrets.sops.env", Requires: "Redeploy"}) {
		t.Errorf("expected config and secrets tracked with redeploy, got %+v", stack.ConfigFiles)
	}
	if stack.ComposeCmdWrapper != ComposeWrapper || stack.RegistryProvider != "10.0.0.200:3000" || stack.WebhookEnabled {
		t.Errorf("unexpected stack wrapper/registry/webhook %+v", stack)
	}
	if len(k.deployed) != 0 {
		t.Errorf("SetupEnv must not deploy anything, got %v", k.deployed)
	}
}

func TestSetupEnv_FailsWhenServersNeverConnect(t *testing.T) {
	k := &fakeKomodo{waitErr: errors.New("komodo servers not connected: [my-app-api-staging]")}
	err := newRollout(&fakeGitea{}, k, &fakeGit{}).SetupEnv(context.Background(), stagingTarget(), envrepo.Repo{Path: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "Periphery") {
		t.Errorf("expected a hint about Periphery, got %v", err)
	}
	if len(k.stacks) != 0 {
		t.Errorf("nothing should be declared before the servers connect, got %v", k.stacks)
	}
}

func TestRelease_TagsMainAndWaitsForEveryImage(t *testing.T) {
	g := &fakeGitea{imagesAfter: 3}
	r := newRollout(g, &fakeKomodo{}, &fakeGit{})
	if err := r.Release(context.Background(), "my-app", services(), semver.Version{Major: 1, Minor: 4, Patch: 2}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if strings.Join(g.tags, ",") != "v1.4.2@main" {
		t.Errorf("expected v1.4.2 tagged on main, got %v", g.tags)
	}
	if g.lookups <= 3 {
		t.Errorf("expected Release to poll until the images exist, got %d lookups", g.lookups)
	}
}

func TestRelease_TimesOutNamingTheMissingImages(t *testing.T) {
	g := &fakeGitea{images: map[string]bool{"my-app-api:1.4.2": true}}
	err := newRollout(g, &fakeKomodo{}, &fakeGit{}).Release(context.Background(), "my-app", services(), semver.Version{Major: 1, Minor: 4, Patch: 2})
	if err == nil || !strings.Contains(err.Error(), "[worker]") || !strings.Contains(err.Error(), "urbex-release") {
		t.Errorf("expected a timeout naming the worker image and the release action, got %v", err)
	}
}

func repoWithVersions(t *testing.T, version string) envrepo.Repo {
	t.Helper()
	repo := envrepo.Repo{Path: t.TempDir()}
	for _, svc := range services() {
		if err := repo.WriteService("staging", "my-app", svc, "img"); err != nil {
			t.Fatal(err)
		}
		if version != "" {
			if err := repo.SetVersion("staging", "my-app", svc.Name, version); err != nil {
				t.Fatal(err)
			}
		}
	}
	return repo
}

func running(version string) map[string]komodo.StackStatus {
	return map[string]komodo.StackStatus{
		"my-app-api-staging":    {State: "running", Images: []string{"10.0.0.200:3000/urbex/my-app-api:" + version}},
		"my-app-worker-staging": {State: "running", Images: []string{"10.0.0.200:3000/urbex/my-app-worker:" + version}},
	}
}

func TestWaitDeployed_ReturnsOnceKomodoRunsTheWantedVersions(t *testing.T) {
	k := &fakeKomodo{status: running("1.4.2")}
	deployed, err := newRollout(&fakeGitea{}, k, &fakeGit{}).WaitDeployed(context.Background(), stagingTarget(), repoWithVersions(t, "1.4.2"))
	if err != nil {
		t.Fatalf("WaitDeployed: %v", err)
	}
	if deployed[0].Service != "api" || deployed[0].Want != "1.4.2" || !strings.HasSuffix(deployed[0].Image, ":1.4.2") {
		t.Errorf("unexpected api status %+v", deployed[0])
	}
	if k.reconciles != 0 {
		t.Errorf("an environment that converged must not be nudged, got %d reconciles", k.reconciles)
	}
}

func TestWaitDeployed_NudgesKomodoWhenAPushWasMissed(t *testing.T) {
	// Komodo still runs the old version: it missed the push. Reconciling
	// makes it catch up.
	k := &fakeKomodo{status: running("1.4.1")}
	k.onReconcile = func() { k.status = running("1.4.2") }

	if _, err := newRollout(&fakeGitea{}, k, &fakeGit{}).WaitDeployed(context.Background(), stagingTarget(), repoWithVersions(t, "1.4.2")); err != nil {
		t.Fatalf("WaitDeployed: %v", err)
	}
	if k.reconciles != 1 {
		t.Errorf("expected exactly one reconcile, got %d", k.reconciles)
	}
}

func TestWaitDeployed_TimesOutSayingWhatIsPending(t *testing.T) {
	status := running("1.4.2")
	status["my-app-worker-staging"] = komodo.StackStatus{State: "unhealthy", Images: []string{"10.0.0.200:3000/urbex/my-app-worker:1.4.1"}}
	k := &fakeKomodo{status: status}
	_, err := newRollout(&fakeGitea{}, k, &fakeGit{}).WaitDeployed(context.Background(), stagingTarget(), repoWithVersions(t, "1.4.2"))
	if err == nil || !strings.Contains(err.Error(), "worker (want 1.4.2, unhealthy") || strings.Contains(err.Error(), "api (") {
		t.Errorf("expected a timeout naming only the worker, got %v", err)
	}
}

func TestStatus_ReportsWantedVersionAndWhatRuns(t *testing.T) {
	k := &fakeKomodo{status: map[string]komodo.StackStatus{"my-app-api-staging": {State: "down"}}}
	deployed, err := newRollout(&fakeGitea{}, k, &fakeGit{}).Status(context.Background(), stagingTarget(), repoWithVersions(t, ""))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if deployed[0] != (Deployed{Service: "api", State: "down"}) {
		t.Errorf("unexpected api status %+v", deployed[0])
	}
	if deployed[1].State != "not_declared" {
		t.Errorf("expected a stack komodo doesn't know to be reported as such, got %+v", deployed[1])
	}
}

func TestDeploy_DeploysEveryStackAndExplainsFailures(t *testing.T) {
	k := &fakeKomodo{}
	r := newRollout(&fakeGitea{}, k, &fakeGit{})
	if err := r.Deploy(context.Background(), stagingTarget()); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if strings.Join(k.deployed, ",") != "my-app-api-staging,my-app-worker-staging" {
		t.Errorf("unexpected deploys %v", k.deployed)
	}
	k.deployFail = true
	if err := r.Deploy(context.Background(), stagingTarget()); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Errorf("expected the failed stage's output in the error, got %v", err)
	}
}

func TestRemove_EnvThenServersThenProject(t *testing.T) {
	const hook = "http://10.0.0.201:9120/listener/github/action/urbex-release/__ANY__"
	g := &fakeGitea{hooks: map[string]string{hook: "x"}}
	k := &fakeKomodo{}
	r := newRollout(g, k, &fakeGit{})

	if err := r.RemoveEnv(context.Background(), stagingTarget()); err != nil {
		t.Fatalf("RemoveEnv: %v", err)
	}
	if strings.Join(k.destroyed, ",") != "my-app-api-staging,my-app-worker-staging" {
		t.Errorf("expected the stacks to be taken down first, got %v", k.destroyed)
	}
	if strings.Join(k.deleted, ",") != "Stack my-app-api-staging,Stack my-app-worker-staging" {
		t.Errorf("unexpected deletions (servers and builds must outlive RemoveEnv): %v", k.deleted)
	}
	if len(g.hooks) != 1 {
		t.Error("the release webhook belongs to the project, not to an environment")
	}

	k.deleted = nil
	if err := r.RemoveServers(context.Background(), stagingTarget()); err != nil {
		t.Fatalf("RemoveServers: %v", err)
	}
	if strings.Join(k.deleted, ",") != "Server my-app-api-staging,Server my-app-worker-staging" {
		t.Errorf("unexpected server deletions %v", k.deleted)
	}

	k.deleted = nil
	if err := r.RemoveProject(context.Background(), "my-app", services()); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if strings.Join(k.deleted, ",") != "Build my-app-api,Build my-app-worker" || len(g.hooks) != 0 {
		t.Errorf("expected builds and the release webhook gone, got %v / %v", k.deleted, g.hooks)
	}
}

func TestEveryRuntimeHasADockerfile(t *testing.T) {
	r := newRollout(&fakeGitea{}, &fakeKomodo{}, &fakeGit{})
	for _, runtime := range []string{"go", "java", "python"} {
		if _, err := r.buildConfig("p", manifest.Service{Name: "s", Runtime: runtime}); err != nil {
			t.Errorf("runtime %s: %v", runtime, err)
		}
	}
}

func TestReconcile_RunsTheGitOpsProcedure(t *testing.T) {
	k := &fakeKomodo{}
	if err := newRollout(&fakeGitea{}, k, &fakeGit{}).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if k.reconciles != 1 {
		t.Errorf("expected one run of the gitops procedure, got %d", k.reconciles)
	}
}
