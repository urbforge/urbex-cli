// Package imagebuild turns an app repo commit into container images:
// it pushes the commit to the app's repo on Gitea, then has Komodo build
// one image per service and push it to Gitea's container registry. See
// docs/decisions/0018-image-build-komodo-gitea-registry.md in
// urbforge/urbex.
//
// Images are tagged with the commit's short SHA, so building is
// idempotent: an image that already exists for a commit (e.g. built for
// staging, now being promoted to prod) is reused, never rebuilt.
package imagebuild

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	platformassets "github.com/urbforge/urbex-cli/platform"
)

// GeneratedDockerfile is where the pre-build step writes the Urbex
// Dockerfile for language runtimes, inside Komodo's clone of the repo.
const GeneratedDockerfile = "Dockerfile.urbex"

// Gitea is the subset of the Gitea client the builder uses.
type Gitea interface {
	EnsureRepo(ctx context.Context, owner, name string) error
	ContainerImageExists(ctx context.Context, owner, name, tag string) (bool, error)
}

// Komodo is the subset of the Komodo client the builder uses.
type Komodo interface {
	EnsureBuild(ctx context.Context, name string, cfg komodo.BuildConfig) error
	RunBuild(ctx context.Context, name string) (*komodo.Update, error)
	WaitUpdate(ctx context.Context, id string, interval time.Duration) (*komodo.Update, error)
}

// Git is the subset of git operations the builder uses on the app repo.
type Git interface {
	HeadCommit(ctx context.Context, dir string) (string, error)
	ShortCommit(ctx context.Context, dir string) (string, error)
	IsDirty(ctx context.Context, dir string) (bool, error)
	Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error
}

// Builder builds project images through Gitea + Komodo.
type Builder struct {
	Gitea     Gitea
	Komodo    Komodo
	Git       Git
	Endpoints bootstrap.Endpoints
	Org       string
	Token     string // Gitea token with write:repository
	RepoDir   string // local working copy of the app repo
	Out       io.Writer
	// PollInterval is how often a running build is polled; 0 means 3s.
	PollInterval time.Duration
}

// Release is the set of images built from one commit of the app repo.
type Release struct {
	Commit string            // full SHA
	Images map[string]string // service name -> image reference
}

// Build pushes HEAD of the app repo to Gitea and returns an image per
// service, building the ones that don't exist yet.
func (b *Builder) Build(ctx context.Context, project string, services []manifest.Service) (*Release, error) {
	commit, err := b.Git.HeadCommit(ctx, b.RepoDir)
	if err != nil {
		return nil, fmt.Errorf("reading HEAD of %s: %w", b.RepoDir, err)
	}
	short, err := b.Git.ShortCommit(ctx, b.RepoDir)
	if err != nil {
		return nil, err
	}
	if dirty, err := b.Git.IsDirty(ctx, b.RepoDir); err == nil && dirty {
		fmt.Fprintf(b.Out, "Warning: %s has uncommitted changes; deploying commit %s without them.\n", b.RepoDir, short)
	}

	if err := b.Gitea.EnsureRepo(ctx, b.Org, project); err != nil {
		return nil, err
	}
	// Gitea's copy of the app repo is what Komodo builds from; main
	// tracks the last commit urbex deployed.
	if err := b.Git.Push(ctx, b.RepoDir, b.Endpoints.RepoURL(b.Org, project), b.Token, "HEAD:refs/heads/main"); err != nil {
		return nil, fmt.Errorf("pushing %s to gitea (if main diverged there, push or merge it first): %w", b.RepoDir, err)
	}

	release := &Release{Commit: commit, Images: map[string]string{}}
	for _, svc := range services {
		image, err := b.buildService(ctx, project, svc, commit, short)
		if err != nil {
			return nil, err
		}
		release.Images[svc.Name] = image
	}
	return release, nil
}

func (b *Builder) buildService(ctx context.Context, project string, svc manifest.Service, commit, short string) (string, error) {
	name := project + "-" + svc.Name
	ref := func(tag string) string { return b.Endpoints.Registry + "/" + b.Org + "/" + name + ":" + tag }

	exists, err := b.Gitea.ContainerImageExists(ctx, b.Org, name, short)
	if err != nil {
		return "", err
	}
	if exists {
		fmt.Fprintf(b.Out, "Image %s already built, reusing it.\n", ref(short))
		return ref(short), nil
	}

	cfg, err := b.buildConfig(project, svc, commit)
	if err != nil {
		return "", err
	}
	if err := b.Komodo.EnsureBuild(ctx, name, cfg); err != nil {
		return "", err
	}
	fmt.Fprintf(b.Out, "Building %s at %s on Komodo...\n", name, short)
	started, err := b.Komodo.RunBuild(ctx, name)
	if err != nil {
		return "", err
	}
	interval := b.PollInterval
	if interval == 0 {
		interval = 3 * time.Second
	}
	done, err := b.Komodo.WaitUpdate(ctx, started.ID.OID, interval)
	if err != nil {
		return "", err
	}
	if !done.Success {
		return "", fmt.Errorf("komodo build %s failed - %s", name, done.FailureSummary())
	}
	tag := done.CommitHash
	if tag == "" {
		tag = short
	}
	fmt.Fprintf(b.Out, "Built %s.\n", ref(tag))
	return ref(tag), nil
}

// buildConfig is the Komodo Build for one service: clone the app repo
// from Gitea at commit, build with the service's own Dockerfile (runtime
// docker) or Urbex's per-runtime one, push to Gitea's registry tagged
// with the commit.
func (b *Builder) buildConfig(project string, svc manifest.Service, commit string) (komodo.BuildConfig, error) {
	cfg := komodo.BuildConfig{
		BuilderID:        bootstrap.KomodoServerName,
		GitProvider:      b.Endpoints.Registry, // Gitea's host:port serves both git and the registry
		GitHTTPS:         false,
		GitAccount:       bootstrap.GiteaAdminUser,
		Repo:             b.Org + "/" + project,
		Branch:           "main",
		Commit:           commit,
		ImageName:        project + "-" + svc.Name,
		IncludeCommitTag: true,
		ImageRegistry: []komodo.ImageRegistry{{
			Domain:       b.Endpoints.Registry,
			Account:      bootstrap.GiteaAdminUser,
			Organization: b.Org,
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

// GitCLI implements Git with the git binary (see internal/gitsync).
type GitCLI struct{}

func (GitCLI) HeadCommit(ctx context.Context, dir string) (string, error) {
	return gitsync.HeadCommit(ctx, dir)
}
func (GitCLI) ShortCommit(ctx context.Context, dir string) (string, error) {
	return gitsync.ShortCommit(ctx, dir)
}
func (GitCLI) IsDirty(ctx context.Context, dir string) (bool, error) {
	return gitsync.IsDirty(ctx, dir)
}
func (GitCLI) Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	return gitsync.Push(ctx, dir, remoteURL, token, refspecs...)
}
