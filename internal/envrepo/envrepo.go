// Package envrepo reads and writes the environments/ tree of the GitOps
// repo, which holds - one folder per environment - how each service
// runs, its configuration, its secrets, and the version to deploy:
//
//	environments/<env>/<project>/<service>/
//	    compose.yaml        generated from urbex.yaml; owned by urbex
//	    version.env         VERSION=1.4.2 (a release) or VERSION=a1b2c3d (a
//	                        commit of main); TRACK=main if it follows main;
//	                        VERSION=17.6@sha256:... (a published image's
//	                        tag and the digest it resolved to)
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
	"regexp"
	"sort"
	"strconv"
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

	// GeneratedFile and GeneratedSecretsFile are what urbex generates for
	// an application that provides or uses something (ADR-0028,
	// Generated variables); they are loaded after the user's files.
	GeneratedFile        = "urbex.env"
	GeneratedSecretsFile = secrets.GeneratedFileName

	// SecretsEnvVar names the variable through which the compose file
	// finds the decrypted secrets at deploy time: Komodo runs `docker
	// compose up` through a wrapper that has sops decrypt them to a
	// temporary file and passes its path here. GeneratedSecretsEnvVar
	// does the same for the generated ones.
	SecretsEnvVar          = "URBEX_SECRETS_FILE"
	GeneratedSecretsEnvVar = "URBEX_GENERATED_SECRETS_FILE"

	// WaitImage runs a Stack's wait for what its application uses: it has
	// pg_isready and a busybox with wget.
	WaitImage = "docker.io/library/postgres:17-alpine"
)

// The wait's probing of a provider (urbforge/urbex-cli#43).
const (
	// WaitProbeInterval is the time between two probes of a provider
	// that answers, while the wait watches it for
	// manifest.ReadyStableSeconds (a sleep argument): short enough to
	// see a restart that lasts less than a second.
	WaitProbeInterval = "0.25"
	// WaitMaxBackoffSeconds caps the doubling wait between the probes of
	// a provider that doesn't answer (plus up to as much again of jitter).
	WaitMaxBackoffSeconds = 8
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

// GeneratedSecretsPath is the path of a service's encrypted generated
// file.
func (r Repo) GeneratedSecretsPath(env, project, service string) string {
	return r.file(env, project, service, GeneratedSecretsFile)
}

// GeneratedKeys are the variables urbex generates for a service in env,
// plain and encrypted: the ones its user can't set (ADR-0030).
func (r Repo) GeneratedKeys(env, project, service string) ([]string, error) {
	plain, err := secrets.Keys(r.file(env, project, service, GeneratedFile))
	if err != nil {
		return nil, err
	}
	kept, err := secrets.Keys(r.GeneratedSecretsPath(env, project, service))
	if err != nil {
		return nil, err
	}
	return append(plain, kept...), nil
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
func (r Repo) WriteService(env, project string, svc manifest.Service, image string, followMain bool, links *Links) error {
	dir := filepath.Join(r.Path, filepath.FromSlash(ServiceDir(env, project, svc.Name)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ComposeFile), []byte(renderCompose(env, project, svc, image, links)), 0o644); err != nil {
		return err
	}
	if links != nil {
		if err := os.WriteFile(filepath.Join(dir, GeneratedFile), []byte(renderGenerated(links.Env)), 0o644); err != nil {
			return err
		}
		if err := ensureGeneratedSecrets(filepath.Join(dir, GeneratedSecretsFile)); err != nil {
			return err
		}
	}
	configPath := filepath.Join(dir, ConfigFile)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(renderConfig(configVars(svc.EnvFor(env)))), 0o644); err != nil {
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
	// commit of main ("a1b2c3d"); for an application from a published
	// image, its tag and digest ("17.6@sha256:..."). "" if nothing is set
	// yet.
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
	if strings.Contains(st.Version, "@") {
		b.WriteString("# What this application runs here: its image's tag and the digest it\n")
		b.WriteString("# resolved to, kept until changed even if the tag moves. Change it - urbex\n")
		b.WriteString("# deploy --update or --version, urbex promote, or by hand - and push:\n")
		b.WriteString("# Komodo deploys it.\n")
	} else {
		b.WriteString("# What this service runs here: a release (VERSION=1.4.2) or a commit of\n")
		b.WriteString("# main (VERSION=a1b2c3d). Change it - urbex deploy, urbex promote, or by\n")
		b.WriteString("# hand - and push: Komodo deploys it.\n")
	}
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
func renderCompose(env, project string, svc manifest.Service, image string, links *Links) string {
	if svc.IsWeb() {
		return renderWebCompose(env, project, image)
	}
	var b strings.Builder
	b.WriteString("# Generated by urbex apply from the project's urbex.yaml - do not edit:\n")
	b.WriteString("# changes are overwritten. The version lives in " + VersionFile + ", configuration\n")
	b.WriteString("# in " + ConfigFile + ", secrets in " + SecretsFile + ".\n")
	b.WriteString("services:\n")
	b.WriteString("  app:\n")
	b.WriteString("    image: " + image + ":${VERSION}\n")
	b.WriteString("    restart: unless-stopped\n")
	writeKindLabels(&b, svc.Name, project, env)
	if links != nil && len(links.Waits) > 0 {
		b.WriteString("    depends_on:\n")
		b.WriteString("      wait:\n")
		b.WriteString("        condition: service_completed_successfully\n")
	}
	writeEnvFiles(&b, links != nil)
	// An application from a published image reads its own variables: it
	// gets no PORT, and publishes its port only if it declares one.
	vars := map[string]string{}
	if !svc.FromImage() {
		vars["PORT"] = fmt.Sprint(svc.Port)
	}
	for k, v := range svc.Extra {
		vars[k] = v
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("    environment:\n")
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "      %s: %q\n", k, vars[k])
	}
	if svc.Port != 0 {
		b.WriteString("    ports:\n")
		fmt.Fprintf(&b, "      - \"%d:%d\"\n", svc.Port, svc.Port)
	}
	if svc.Provides == manifest.InterfacePostgres {
		b.WriteString("    ports:\n")
		b.WriteString("      - \"5432:5432\"\n")
	}
	// A named volume is a Docker named volume on the folder its mount
	// point of the LXC is (ADR-0028). Docker fills it, the first time,
	// with what the image keeps at path and its owners, so an image that
	// doesn't run as root can write to it. A container doesn't start
	// when the folder isn't there, so it can't write to the LXC's own
	// disk. A share is a folder of the node, bound as it is.
	if len(svc.Volumes) > 0 {
		b.WriteString("    volumes:\n")
	}
	for _, v := range svc.Volumes {
		switch {
		case v.Share != "":
			ro := ""
			if v.ReadOnly {
				ro = ":ro"
			}
			fmt.Fprintf(&b, "      - %s:%s%s\n", svc.LXCPath(v), v.Path, ro)
		default:
			fmt.Fprintf(&b, "      - %s:%s\n", v.Name, v.Path)
		}
	}
	if svc.Healthcheck != "" && svc.Port != 0 {
		b.WriteString("    healthcheck:\n")
		fmt.Fprintf(&b, "      test: [\"CMD\", \"wget\", \"-q\", \"--spider\", \"http://localhost:%d%s\"]\n", svc.Port, svc.Healthcheck)
		b.WriteString("      interval: 30s\n")
		b.WriteString("      timeout: 5s\n")
		b.WriteString("      retries: 3\n")
	}
	if svc.Provides == manifest.InterfacePostgres {
		// The official image has no healthcheck, and a running container
		// would let the applications that use it start while initdb runs
		// (ADR-0030): it is healthy when the server takes connections.
		b.WriteString("    healthcheck:\n")
		b.WriteString("      test: [\"CMD-SHELL\", \"pg_isready -h 127.0.0.1 -U $$POSTGRES_USER -d $$POSTGRES_DB\"]\n")
		b.WriteString("      interval: 5s\n")
		b.WriteString("      timeout: 5s\n")
		b.WriteString("      retries: 30\n")
	}
	if links != nil && len(links.Waits) > 0 {
		writeWait(&b, links)
	}
	if named := svc.NamedVolumes(); len(named) > 0 {
		b.WriteString("volumes:\n")
		for _, v := range named {
			fmt.Fprintf(&b, "  %s:\n", v.Name)
			b.WriteString("    driver: local\n")
			b.WriteString("    driver_opts:\n")
			b.WriteString("      type: none\n")
			b.WriteString("      o: bind\n")
			fmt.Fprintf(&b, "      device: %s\n", svc.LXCPath(v))
		}
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

// renderWebCompose is the compose file of a web frontend: its image
// uploads the site to the environment's Cloudflare Worker (named, with
// the credentials, by the Stack's environment) and stays up, healthy once
// the upload worked.
func renderWebCompose(env, project, image string) string {
	var b strings.Builder
	b.WriteString("# Generated by urbex apply from the project's urbex.yaml - do not edit:\n")
	b.WriteString("# changes are overwritten. The version lives in " + VersionFile + ". This\n")
	b.WriteString("# deploys the web frontend to a Cloudflare Worker (ADR-0022).\n")
	b.WriteString("services:\n")
	b.WriteString("  deploy:\n")
	b.WriteString("    image: " + image + ":${VERSION}\n")
	b.WriteString("    restart: unless-stopped\n")
	writeKindLabels(&b, manifest.WebService, project, env)
	b.WriteString("    environment:\n")
	for _, v := range []string{"WORKER_NAME", "WORKER_HOSTNAME", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"} {
		b.WriteString("      " + v + ": ${" + v + "}\n")
	}
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: [\"CMD\", \"test\", \"-f\", \"/tmp/deployed\"]\n")
	b.WriteString("      interval: 10s\n")
	b.WriteString("      start_period: 5m\n")
	b.WriteString("      retries: 3\n")
	return b.String()
}

// writeKindLabels writes the Docker labels the telemetry agent labels a
// project container's logs and metrics with (ADR-0027): an application's,
// even when it runs on a platform LXC, as the web frontends' deploy
// container does on the builder.
func writeKindLabels(b *strings.Builder, service, project, env string) {
	b.WriteString("    labels:\n")
	fmt.Fprintf(b, "      urbex.kind: %q\n", "app")
	fmt.Fprintf(b, "      urbex.service: %q\n", service)
	fmt.Fprintf(b, "      urbex.project: %q\n", project)
	fmt.Fprintf(b, "      urbex.env: %q\n", env)
}

// Deployment is a project's environment in the GitOps repo, with the
// services that have a folder there.
type Deployment struct {
	Env, Project string
	Services     []string
}

// Deployments lists every project environment in the repo, sorted by
// environment, then project.
func (r Repo) Deployments() ([]Deployment, error) {
	var out []Deployment
	envs, err := os.ReadDir(filepath.Join(r.Path, "environments"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		projects, err := os.ReadDir(filepath.Join(r.Path, "environments", env.Name()))
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if !p.IsDir() {
				continue
			}
			services, err := os.ReadDir(filepath.Join(r.Path, "environments", env.Name(), p.Name()))
			if err != nil {
				return nil, err
			}
			d := Deployment{Env: env.Name(), Project: p.Name()}
			for _, s := range services {
				if s.IsDir() {
					d.Services = append(d.Services, s.Name())
				}
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// Port returns the port a service's compose file publishes, or 0.
func (r Repo) Port(env, project, service string) int {
	data, err := os.ReadFile(r.file(env, project, service, ComposeFile))
	if err != nil {
		return 0
	}
	m := publishedPort.FindSubmatch(data)
	if m == nil {
		return 0
	}
	port, _ := strconv.Atoi(string(m[1]))
	return port
}

var publishedPort = regexp.MustCompile(`(?m)^\s+- "(\d+):\d+"`)

// Links is what an application that provides or uses something gets in
// its folder besides the user's files (ADR-0028): the variables urbex
// generates for it, and what its Stack waits for before the application
// starts.
type Links struct {
	// Env are the generated variables that hold no password, written to
	// urbex.env. The ones that do are encrypted into urbex.sops.env by
	// whoever keeps the secrets.
	Env map[string]string
	// Waits are the applications this one uses, one probe each.
	Waits []Wait
	// WaitSeconds is how long the wait tries before giving up: the
	// application's depth in the graph times the budget.
	WaitSeconds int
}

// Wait is an application another one uses, as the wait probes it: for
// a postgres provider, that it takes connections; for any other, its
// healthcheck, which the manifest requires of it (a port answers before
// the application can take calls).
type Wait struct {
	Name      string
	Interface string
	Host      string
	Port      int
	// Healthcheck is the provider's HTTP healthcheck path, "" for none.
	Healthcheck string
}

// writeEnvFiles lists the files compose loads, later ones winning: the
// user's, then the generated ones.
func writeEnvFiles(b *strings.Builder, generated bool) {
	b.WriteString("    env_file:\n")
	b.WriteString("      - " + ConfigFile + "\n")
	b.WriteString("      - path: ${" + SecretsEnvVar + ":-secrets.env}\n")
	b.WriteString("        required: false\n")
	if generated {
		b.WriteString("      - " + GeneratedFile + "\n")
		b.WriteString("      - path: ${" + GeneratedSecretsEnvVar + ":-urbex.secrets.env}\n")
		b.WriteString("        required: false\n")
	}
}

// writeWait writes the service that holds the application back until
// what it uses answers. It exits once they do (or gives up and fails the
// start), and the Stack ignores it once done (ignore_services). It lives
// in the Stack, so it holds for every way the Stack is deployed, the
// push of the GitOps repo included.
func writeWait(b *strings.Builder, l *Links) {
	b.WriteString("\n  # Holds the application back until what it uses answers; see\n")
	b.WriteString("  # docs/decisions/0028-application-model.md (Order between stacks).\n")
	b.WriteString("  wait:\n")
	b.WriteString("    image: " + WaitImage + "\n")
	b.WriteString("    restart: \"no\"\n")
	b.WriteString("    command:\n")
	b.WriteString("      - sh\n")
	b.WriteString("      - -c\n")
	b.WriteString("      - |\n")
	fmt.Fprintf(b, "        end=$$(( $$(date +%%s) + %d ))\n", l.WaitSeconds)
	// Ready once the provider has answered every probe, a quarter of a
	// second apart, for manifest.ReadyStableSeconds: a failed answer
	// starts the window over, so a provider in a crash loop shorter than
	// the window never passes. Every failure counts, a DNS hiccup too:
	// asking again would step over a restart as short as the time between
	// the two. The address is looked up when a window starts and probed
	// as is through it, so that a probe is quick and the probes close
	// together. While the provider doesn't answer, the probes back off
	// exponentially, with jitter, so that the consumers of a provider
	// that comes back don't all probe it at once. Each provider's probe
	// is the function probe, given the address.
	b.WriteString("        wait_for() {\n")
	b.WriteString("          since= delay=1\n")
	b.WriteString("          while :; do\n")
	b.WriteString("            now=$$(date +%s)\n")
	b.WriteString("            [ -z \"$$since\" ] && ip=$$(getent hosts \"$$host\" | awk '{ print $$1; exit }')\n")
	b.WriteString("            if [ -n \"$$ip\" ] && probe \"$$ip\"; then\n")
	b.WriteString("              [ -z \"$$since\" ] && since=$$now\n")
	fmt.Fprintf(b, "              [ $$((now - since)) -ge %d ] && break\n", manifest.ReadyStableSeconds)
	b.WriteString("              delay=1\n")
	b.WriteString("            else\n")
	b.WriteString("              since=\n")
	b.WriteString("            fi\n")
	b.WriteString("            [ $$(date +%s) -ge $$end ] && { echo \"wait: gave up waiting for $$name\"; return 1; }\n")
	fmt.Fprintf(b, "            if [ -n \"$$since\" ]; then sleep %s; continue; fi\n", WaitProbeInterval)
	b.WriteString("            sleep $$((delay + $$RANDOM % (delay + 1)))\n")
	fmt.Fprintf(b, "            [ $$delay -lt %d ] && delay=$$((delay * 2))\n", WaitMaxBackoffSeconds)
	b.WriteString("          done\n")
	b.WriteString("          echo \"wait: $$name is ready\"\n")
	b.WriteString("        }\n")
	for _, w := range l.Waits {
		if w.Interface == manifest.InterfacePostgres {
			fmt.Fprintf(b, "        name=%s host=%s; probe() { pg_isready -q -t 2 -h \"$$1\" -p %d; }; wait_for || exit 1\n", w.Name, w.Host, w.Port)
			continue
		}
		// To the address, as the provider's name.
		fmt.Fprintf(b, "        name=%s host=%s; probe() { wget -q --spider -T 2 --header \"Host: %s:%d\" \"http://$$1:%d%s\"; }; wait_for || exit 1\n",
			w.Name, w.Host, w.Host, w.Port, w.Port, w.Healthcheck)
	}
}

// configVars are the variables of env that go to config.env: the ones
// with a reference to something urbex generates are resolved at apply and
// kept in the generated files.
func configVars(vars map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range vars {
		if len(manifest.References(v)) == 0 {
			out[k] = v
		}
	}
	return out
}

func renderGenerated(vars map[string]string) string {
	var b strings.Builder
	b.WriteString("# Generated by urbex apply - do not edit: it is rewritten by every apply.\n")
	b.WriteString("# It holds what urbex works out for this application (connection\n")
	b.WriteString("# variables, a provider's settings). Passwords are in " + GeneratedSecretsFile + ".\n")
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

// ensureGeneratedSecrets creates the encrypted generated file, empty, if
// it doesn't exist: Komodo tracks it and refuses to deploy a Stack with a
// tracked file missing.
func ensureGeneratedSecrets(path string) error {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(secrets.GeneratedPlaceholder), 0o644)
}
