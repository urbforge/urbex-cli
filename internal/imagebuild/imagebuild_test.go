package imagebuild

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type fakeGitea struct {
	repos    []string
	existing map[string]bool // "name:tag"
}

func (f *fakeGitea) EnsureRepo(ctx context.Context, owner, name string) error {
	f.repos = append(f.repos, owner+"/"+name)
	return nil
}
func (f *fakeGitea) ContainerImageExists(ctx context.Context, owner, name, tag string) (bool, error) {
	return f.existing[name+":"+tag], nil
}

type fakeKomodo struct {
	builds map[string]komodo.BuildConfig
	runs   []string
	fail   bool
}

func (f *fakeKomodo) EnsureBuild(ctx context.Context, name string, cfg komodo.BuildConfig) error {
	if f.builds == nil {
		f.builds = map[string]komodo.BuildConfig{}
	}
	f.builds[name] = cfg
	return nil
}
func (f *fakeKomodo) RunBuild(ctx context.Context, name string) (*komodo.Update, error) {
	f.runs = append(f.runs, name)
	u := &komodo.Update{Status: "InProgress"}
	u.ID.OID = "u-" + name
	return u, nil
}
func (f *fakeKomodo) WaitUpdate(ctx context.Context, id string, interval time.Duration) (*komodo.Update, error) {
	if f.fail {
		return &komodo.Update{Status: "Complete", Logs: []komodo.Log{{Stage: "Docker Build", Stderr: "no main package"}}}, nil
	}
	return &komodo.Update{Status: "Complete", Success: true, CommitHash: "abc1234"}, nil
}

type fakeGit struct {
	pushes []string
	dirty  bool
}

func (f *fakeGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	return "abc1234def5678", nil
}
func (f *fakeGit) ShortCommit(ctx context.Context, dir string) (string, error) { return "abc1234", nil }
func (f *fakeGit) IsDirty(ctx context.Context, dir string) (bool, error)       { return f.dirty, nil }
func (f *fakeGit) Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	f.pushes = append(f.pushes, remoteURL+" "+strings.Join(refspecs, " "))
	return nil
}

func newBuilder(g *fakeGitea, k *fakeKomodo, git *fakeGit, out *bytes.Buffer) *Builder {
	return &Builder{
		Gitea:  g,
		Komodo: k,
		Git:    git,
		Endpoints: bootstrap.Endpoints{
			GiteaURL: "http://10.0.0.200:3000",
			Registry: "10.0.0.200:3000",
		},
		Org:          "urbex",
		Token:        "tok",
		RepoDir:      "/app",
		Out:          out,
		PollInterval: time.Millisecond,
	}
}

func TestBuild_PushesRepoAndBuildsEachService(t *testing.T) {
	g, k, git, out := &fakeGitea{}, &fakeKomodo{}, &fakeGit{dirty: true}, &bytes.Buffer{}
	services := []manifest.Service{
		{Name: "api", Runtime: "go", Port: 8080},
		{Name: "worker", Runtime: "docker", Dockerfile: "./worker/Dockerfile", Port: 9000},
	}

	release, err := newBuilder(g, k, git, out).Build(context.Background(), "my-app", services)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if release.Commit != "abc1234def5678" {
		t.Errorf("Commit = %q", release.Commit)
	}
	if got := release.Images["api"]; got != "10.0.0.200:3000/urbex/my-app-api:abc1234" {
		t.Errorf("api image = %q", got)
	}
	if strings.Join(g.repos, ",") != "urbex/my-app" {
		t.Errorf("expected the app repo to be ensured on gitea, got %v", g.repos)
	}
	if len(git.pushes) != 1 || git.pushes[0] != "http://10.0.0.200:3000/urbex/my-app.git HEAD:refs/heads/main" {
		t.Errorf("unexpected pushes %v", git.pushes)
	}
	if !strings.Contains(out.String(), "uncommitted changes") {
		t.Errorf("expected a dirty-repo warning, got:\n%s", out)
	}

	api := k.builds["my-app-api"]
	if api.Commit != "abc1234def5678" || api.Repo != "urbex/my-app" || api.GitProvider != "10.0.0.200:3000" || !api.IncludeCommitTag {
		t.Errorf("unexpected api build config %+v", api)
	}
	if api.DockerfilePath != GeneratedDockerfile || !strings.Contains(api.PreBuild.Command, "golang") || !api.PreBuild.ShellMode {
		t.Errorf("expected the go runtime Dockerfile to be written by pre_build, got %+v", api.PreBuild)
	}
	if len(api.ImageRegistry) != 1 || api.ImageRegistry[0].Organization != "urbex" {
		t.Errorf("unexpected registry config %+v", api.ImageRegistry)
	}

	worker := k.builds["my-app-worker"]
	if worker.BuildPath != "worker" || worker.DockerfilePath != "Dockerfile" || worker.PreBuild.Command != "" {
		t.Errorf("expected the service's own Dockerfile with its directory as context, got %+v", worker)
	}
}

func TestBuild_ReusesExistingImage(t *testing.T) {
	g := &fakeGitea{existing: map[string]bool{"my-app-api:abc1234": true}}
	k := &fakeKomodo{}
	release, err := newBuilder(g, k, &fakeGit{}, &bytes.Buffer{}).Build(context.Background(), "my-app",
		[]manifest.Service{{Name: "api", Runtime: "go", Port: 8080}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(k.runs) != 0 {
		t.Errorf("expected no komodo build for an existing image, got %v", k.runs)
	}
	if release.Images["api"] != "10.0.0.200:3000/urbex/my-app-api:abc1234" {
		t.Errorf("unexpected image %q", release.Images["api"])
	}
}

func TestBuild_FailureSurfacesKomodoLogs(t *testing.T) {
	_, err := newBuilder(&fakeGitea{}, &fakeKomodo{fail: true}, &fakeGit{}, &bytes.Buffer{}).Build(context.Background(), "my-app",
		[]manifest.Service{{Name: "api", Runtime: "go", Port: 8080}})
	if err == nil || !strings.Contains(err.Error(), "no main package") {
		t.Errorf("expected the failed stage's output in the error, got %v", err)
	}
}

func TestEveryRuntimeHasADockerfile(t *testing.T) {
	for _, runtime := range []string{"go", "java", "python"} {
		b := newBuilder(&fakeGitea{}, &fakeKomodo{}, &fakeGit{}, &bytes.Buffer{})
		if _, err := b.buildConfig("p", manifest.Service{Name: "s", Runtime: runtime}, "c"); err != nil {
			t.Errorf("runtime %s: %v", runtime, err)
		}
	}
}
