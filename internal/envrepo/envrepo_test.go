package envrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/secrets"
)

func service() manifest.Service {
	return manifest.Service{
		Name: "api", Runtime: "go", Port: 8080, Healthcheck: "/healthz",
		Env: map[string]map[string]string{"staging": {"LOG_LEVEL": "debug", "A": "1"}, "prod": {"LOG_LEVEL": "info"}},
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteService_GeneratesComposeAndSeedsConfigOnce(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	dir := filepath.Join(repo.Path, "environments", "staging", "my-app", "api")
	const image = "10.0.0.200:3000/urbex/my-app-api"

	if err := repo.WriteService("staging", "my-app", service(), image, false); err != nil {
		t.Fatalf("WriteService: %v", err)
	}

	compose := read(t, filepath.Join(dir, ComposeFile))
	for _, want := range []string{
		"image: " + image + ":${VERSION}",
		"- config.env",
		"- path: ${URBEX_SECRETS_FILE:-secrets.env}",
		"required: false",
		`PORT: "8080"`,
		`- "8080:8080"`,
		`"http://localhost:8080/healthz"`,
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("expected %q in compose.yaml:\n%s", want, compose)
		}
	}
	if config := read(t, filepath.Join(dir, ConfigFile)); !strings.HasSuffix(config, "A=1\nLOG_LEVEL=debug\n") {
		t.Errorf("expected config.env seeded from the manifest's staging env, got:\n%s", config)
	}
	if got := read(t, filepath.Join(dir, SecretsFile)); got != secrets.Placeholder {
		t.Errorf("expected an empty secrets file, got:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, VersionFile)); !os.IsNotExist(err) {
		t.Errorf("a new service must have no version yet, stat err = %v", err)
	}

	// The GitOps repo is authoritative from here on: a second apply,
	// with a changed manifest, regenerates compose.yaml only.
	os.WriteFile(filepath.Join(dir, ConfigFile), []byte("LOG_LEVEL=warn\n"), 0o644)
	os.WriteFile(filepath.Join(dir, SecretsFile), []byte("X=ENC[..]\nsops_version=3\n"), 0o644)
	changed := service()
	changed.Port = 9090
	changed.Healthcheck = ""
	if err := repo.WriteService("staging", "my-app", changed, image, false); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	compose = read(t, filepath.Join(dir, ComposeFile))
	if !strings.Contains(compose, `- "9090:9090"`) || strings.Contains(compose, "healthcheck") {
		t.Errorf("expected compose.yaml regenerated from the changed manifest:\n%s", compose)
	}
	if got := read(t, filepath.Join(dir, ConfigFile)); got != "LOG_LEVEL=warn\n" {
		t.Errorf("config.env must not be overwritten, got:\n%s", got)
	}
	if got := read(t, filepath.Join(dir, SecretsFile)); !strings.Contains(got, "sops_version") {
		t.Errorf("secrets must not be overwritten, got:\n%s", got)
	}
}

func TestVersion(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	pinned := VersionFileState{Version: "1.4.2"}
	if err := repo.SetVersion("prod", "my-app", "api", pinned); err == nil || !strings.Contains(err.Error(), "urbex apply prod") {
		t.Errorf("expected setting a version before apply to fail with a hint, got %v", err)
	}
	if err := repo.WriteService("prod", "my-app", service(), "img", false); err != nil {
		t.Fatal(err)
	}
	if st, err := repo.Version("prod", "my-app", "api"); err != nil || st != (VersionFileState{}) {
		t.Errorf("Version before any deploy = %+v, %v; want nothing", st, err)
	}
	if err := repo.SetVersion("prod", "my-app", "api", pinned); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}
	if st, err := repo.Version("prod", "my-app", "api"); err != nil || st != pinned {
		t.Errorf("Version = %+v, %v; want %+v", st, err, pinned)
	}
	got := read(t, filepath.Join(repo.Path, "environments/prod/my-app/api/version.env"))
	if !strings.HasSuffix(got, "\nVERSION=1.4.2\n") || strings.Contains(got, "TRACK") {
		t.Errorf("version.env must be a dotenv compose can load, without TRACK, got:\n%s", got)
	}
}

func TestVersion_FollowingMain(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	if err := repo.WriteService("staging", "my-app", service(), "img", true); err != nil {
		t.Fatal(err)
	}
	if st, _ := repo.Version("staging", "my-app", "api"); st != (VersionFileState{Track: true}) {
		t.Errorf("a new environment that follows main must have TRACK and no version yet, got %+v", st)
	}
	// What the release Action does after a build of main...
	following := VersionFileState{Version: "a1b2c3d", Track: true}
	if err := repo.SetVersion("staging", "my-app", "api", following); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(repo.Path, "environments/staging/my-app/api/version.env"))
	if !strings.Contains(got, "\nVERSION=a1b2c3d\n") || !strings.HasSuffix(got, "\nTRACK=main\n") {
		t.Errorf("unexpected version.env:\n%s", got)
	}
	if st, _ := repo.Version("staging", "my-app", "api"); st != following {
		t.Errorf("Version = %+v, want %+v", st, following)
	}
	// ...and an existing version file is never reset by a re-apply.
	if err := repo.WriteService("staging", "my-app", service(), "img", true); err != nil {
		t.Fatal(err)
	}
	if st, _ := repo.Version("staging", "my-app", "api"); st != following {
		t.Errorf("re-applying must keep the version file, got %+v", st)
	}
}

func TestEnvironmentsAndRemoveProject(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	if envs, err := repo.Environments("my-app"); err != nil || len(envs) != 0 {
		t.Errorf("Environments on an empty repo = %v, %v", envs, err)
	}
	for _, env := range []string{"staging", "prod"} {
		if err := repo.WriteService(env, "my-app", service(), "img", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WriteService("staging", "other", service(), "img", false); err != nil {
		t.Fatal(err)
	}
	if envs, _ := repo.Environments("my-app"); strings.Join(envs, ",") != "prod,staging" {
		t.Errorf("Environments = %v, want prod and staging", envs)
	}
	if err := repo.RemoveProject("staging", "my-app"); err != nil {
		t.Fatal(err)
	}
	if repo.Exists("staging", "my-app") || !repo.Exists("staging", "other") || !repo.Exists("prod", "my-app") {
		t.Error("RemoveProject must remove exactly one project's folder in one environment")
	}
	if envs, _ := repo.Environments("my-app"); strings.Join(envs, ",") != "prod" {
		t.Errorf("Environments = %v, want prod", envs)
	}
}

func TestServiceDirUsesForwardSlashes(t *testing.T) {
	if got := ServiceDir("staging", "my-app", "api"); got != "environments/staging/my-app/api" {
		t.Errorf("ServiceDir = %q", got)
	}
}

func TestDeploymentsAndPort(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	api := manifest.Service{Name: "api", Runtime: "go", Port: 8080}
	for _, env := range []string{"staging", "prod"} {
		if err := repo.WriteService(env, "hello", api, "reg/urbex/hello-api", env == "staging"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WriteService("staging", "other", manifest.Service{Name: "worker", Runtime: "go", Port: 9000}, "reg/urbex/other-worker", false); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Deployments()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Env+"/"+d.Project+"/"+strings.Join(d.Services, ","))
	}
	if strings.Join(names, " ") != "prod/hello/api staging/hello/api staging/other/worker" {
		t.Errorf("deployments = %v", names)
	}
	if p := repo.Port("staging", "hello", "api"); p != 8080 {
		t.Errorf("port = %d", p)
	}
	if p := repo.Port("staging", "nope", "api"); p != 0 {
		t.Errorf("a missing service has no port, got %d", p)
	}
}
