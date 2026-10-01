// Package envrepo reads and writes the environments/ tree of the GitOps
// repo, which holds - one folder per environment - how each service
// runs, its configuration, its secrets, and the version to deploy:
//
//	environments/<env>/<project>/<service>/
//	    compose.yaml        generated from urbex.yaml; owned by urbex
//	    version.env         VERSION=1.4.2 (a release) or VERSION=a1b2c3d (a
//	                        commit of main); TRACK=main if it follows main
//	    config.env          non-secret configuration
//	    secrets.sops.env    secrets, SOPS-encrypted once there are any
//
// Komodo deploys each of these folders as a Stack. An environment that
// follows main (staging, by default) gets its VERSION rewritten by Komodo
// after every build of main. See
// docs/decisions/0020-trunk-releases-gitops-environments.md and
// docs/decisions/0021-staging-follows-main.md in urbforge/urbex.
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
// (from the manifest's env block), an empty secrets file, and - if
// followMain - a version file that follows main, only if they don't
// exist yet. Afterwards the GitOps repo is where an environment's
// configuration, secrets, and version are changed.
func (r Repo) WriteService(env, project string, svc manifest.Service, image string, followMain bool) error {
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
	if err := secrets.EnsureFile(filepath.Join(dir, SecretsFile)); err != nil {
		return err
	}
	versionPath := filepath.Join(dir, VersionFile)
	if _, err := os.Stat(versionPath); os.IsNotExist(err) && followMain {
		return writeVersionFile(versionPath, VersionFileState{Track: true})
	}
	return nil
}

// VersionFileState is what a service's version.env says.
type VersionFileState struct {
	// Version is what to run: a release ("1.4.2") or the short hash of a
	// commit of main ("a1b2c3d"); "" if nothing is set yet.
	Version string
	// Track is whether the environment follows main: Komodo then sets
	// Version to every new build of main.
	Track bool
}

// Version returns what a service's version.env says in env; the zero
// value if it has none.
func (r Repo) Version(env, project, service string) (VersionFileState, error) {
	data, err := os.ReadFile(r.file(env, project, service, VersionFile))
	if os.IsNotExist(err) {
		return VersionFileState{}, nil
	}
	if err != nil {
		return VersionFileState{}, err
	}
	var st VersionFileState
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "VERSION="); ok {
			st.Version = v
		}
		if line == "TRACK=main" {
			st.Track = true
		}
	}
	return st, nil
}

// SetVersion sets what a service runs in env. The service's folder must
// already exist (urbex apply creates it).
func (r Repo) SetVersion(env, project, service string, st VersionFileState) error {
	p := r.file(env, project, service, VersionFile)
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		return fmt.Errorf("%s/%s has no folder in environment %s - run 'urbex apply %s' first", project, service, env, env)
	}
	return writeVersionFile(p, st)
}

func writeVersionFile(path string, st VersionFileState) error {
	var b strings.Builder
	b.WriteString("# What this service runs here: a release (VERSION=1.4.2) or a commit of\n")
	b.WriteString("# main (VERSION=a1b2c3d). Change it - urbex deploy, urbex promote, or by\n")
	b.WriteString("# hand - and push: Komodo deploys it.\n")
	if st.Track {
		b.WriteString("#\n# TRACK=main: Komodo sets VERSION to every new build of main. Remove the\n")
		b.WriteString("# line (urbex deploy --version does) to stay on a version.\n")
	}
	if st.Version != "" {
		b.WriteString("VERSION=" + st.Version + "\n")
	}
	if st.Track {
		b.WriteString("TRACK=main\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
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
