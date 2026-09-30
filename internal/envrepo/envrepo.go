// Package envrepo reads and writes the environments/ tree of the GitOps
// repo, which holds - one folder per environment - how each service
// runs, its configuration, its secrets, and the version to deploy:
//
//	environments/<env>/<project>/<service>/
//	    compose.yaml        generated from urbex.yaml; owned by urbex
//	    version.env         VERSION=1.4.2 - the release to deploy
//	    config.env          non-secret configuration
//	    secrets.sops.env    secrets, SOPS-encrypted once there are any
//
// Komodo deploys each of these folders as a Stack. See
// docs/decisions/0020-trunk-releases-gitops-environments.md in
// urbforge/urbex.
package envrepo

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/secrets"
)

// Names of the files in a service's folder.
const (
	ComposeFile = "compose.yaml"
	VersionFile = "version.env"
	ConfigFile  = "config.env"
	SecretsFile = secrets.FileName

	// SecretsEnvVar names the variable through which the compose file
	// finds the decrypted secrets at deploy time: Komodo runs `docker
	// compose up` through a wrapper that has sops decrypt them to a
	// temporary file and passes its path here.
	SecretsEnvVar = "URBEX_SECRETS_FILE"
)

// Root is the environments tree, relative to the GitOps repo root.
const Root = "environments"

// ServiceDir is a service's folder relative to the GitOps repo root, with
// forward slashes - as Komodo wants its Stack run directory.
func ServiceDir(env, project, service string) string {
	return path.Join(Root, env, project, service)
}

// ProjectDir is a project's folder in an environment, relative to the
// GitOps repo root.
func ProjectDir(env, project string) string {
	return path.Join(Root, env, project)
}

// Repo is a working copy of the GitOps repo.
type Repo struct {
	Path string
}

func (r Repo) file(env, project, service, name string) string {
	return filepath.Join(r.Path, filepath.FromSlash(ServiceDir(env, project, service)), name)
}

// SecretsPath is the path of a service's encrypted secrets file.
func (r Repo) SecretsPath(env, project, service string) string {
	return r.file(env, project, service, SecretsFile)
}

// Exists reports whether a project has a folder in env, i.e. has been
// applied there.
func (r Repo) Exists(env, project string) bool {
	info, err := os.Stat(filepath.Join(r.Path, filepath.FromSlash(ProjectDir(env, project))))
	return err == nil && info.IsDir()
}

// WriteService writes what urbex owns in a service's folder: it always
// regenerates compose.yaml from the manifest, and creates config.env
// (from the manifest's env block) and an empty secrets file only if they
// don't exist yet - afterwards the GitOps repo is where an environment's
// configuration and secrets are changed.
func (r Repo) WriteService(env, project string, svc manifest.Service, image string) error {
	dir := filepath.Join(r.Path, filepath.FromSlash(ServiceDir(env, project, svc.Name)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ComposeFile), []byte(renderCompose(svc, image)), 0o644); err != nil {
		return err
	}
	configPath := filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(renderConfig(svc.EnvFor(env))), 0o644); err != nil {
			return err
		}
	}
	return secrets.EnsureFile(filepath.Join(dir, SecretsFile))
}

// Version returns the version a service is set to run in env, or "" if
// none has been set yet.
func (r Repo) Version(env, project, service string) (string, error) {
	data, err := os.ReadFile(r.file(env, project, service, VersionFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION="); ok {
			return v, nil
		}
	}
	return "", nil
}

// SetVersion sets the version a service runs in env. The service's
// folder must already exist (urbex apply creates it).
func (r Repo) SetVersion(env, project, service, version string) error {
	p := r.file(env, project, service, VersionFile)
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		return fmt.Errorf("%s/%s has no folder in environment %s - run 'urbex apply %s' first", project, service, env, env)
	}
	content := "# The release of this service to run here. Change it (urbex deploy,\n" +
		"# urbex promote, or by hand) and push: Komodo deploys it.\n" +
		"VERSION=" + version + "\n"
	return os.WriteFile(p, []byte(content), 0o644)
}

// RemoveProject deletes a project's folder in env.
func (r Repo) RemoveProject(env, project string) error {
	return os.RemoveAll(filepath.Join(r.Path, filepath.FromSlash(ProjectDir(env, project))))
}

// Environments lists the environments in which project has a folder.
func (r Repo) Environments(project string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.Path, Root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envs []string
	for _, e := range entries {
		if e.IsDir() && r.Exists(e.Name(), project) {
			envs = append(envs, e.Name())
		}
	}
	return envs, nil
}

// renderCompose is a service's compose file. The image tag and the
// secrets are left to deploy time: VERSION comes from version.env, which
// Komodo passes to compose as an --env-file, and the decrypted secrets
// file from SecretsEnvVar (absent, and then skipped, without secrets).
func renderCompose(svc manifest.Service, image string) string {
	var b strings.Builder
	b.WriteString("# Generated by urbex apply from the project's urbex.yaml - do not edit:\n")
	b.WriteString("# changes are overwritten. The version lives in " + VersionFile + ", configuration\n")
	b.WriteString("# in " + ConfigFile + ", secrets in " + SecretsFile + ".\n")
	b.WriteString("services:\n")
	b.WriteString("  app:\n")
	b.WriteString("    image: " + image + ":${VERSION}\n")
	b.WriteString("    restart: unless-stopped\n")
	b.WriteString("    env_file:\n")
	b.WriteString("      - " + ConfigFile + "\n")
	b.WriteString("      - path: ${" + SecretsEnvVar + ":-secrets.env}\n")
	b.WriteString("        required: false\n")
	b.WriteString("    environment:\n")
	fmt.Fprintf(&b, "      PORT: \"%d\"\n", svc.Port)
	b.WriteString("    ports:\n")
	fmt.Fprintf(&b, "      - \"%d:%d\"\n", svc.Port, svc.Port)
	if svc.Healthcheck != "" {
		b.WriteString("    healthcheck:\n")
		fmt.Fprintf(&b, "      test: [\"CMD\", \"wget\", \"-q\", \"--spider\", \"http://localhost:%d%s\"]\n", svc.Port, svc.Healthcheck)
		b.WriteString("      interval: 30s\n")
		b.WriteString("      timeout: 5s\n")
		b.WriteString("      retries: 3\n")
	}
	return b.String()
}

func renderConfig(vars map[string]string) string {
	var b strings.Builder
	b.WriteString("# Non-secret configuration of this service in this environment, as\n")
	b.WriteString("# KEY=value lines. Edit it here and push: Komodo redeploys. Secrets go\n")
	b.WriteString("# in " + SecretsFile + " (urbex secret set).\n")
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + vars[k] + "\n")
	}
	return b.String()
}
