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

	if err := repo.WriteService("staging", "my-app", service(), image, false, nil); err != nil {
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
	if err := repo.WriteService("staging", "my-app", changed, image, false, nil); err != nil {
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
	if err := repo.WriteService("prod", "my-app", service(), "img", false, nil); err != nil {
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
	if err := repo.WriteService("staging", "my-app", service(), "img", true, nil); err != nil {
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
	if err := repo.WriteService("staging", "my-app", service(), "img", true, nil); err != nil {
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
		if err := repo.WriteService(env, "my-app", service(), "img", false, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WriteService("staging", "other", service(), "img", false, nil); err != nil {
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
		if err := repo.WriteService(env, "hello", api, "reg/urbex/hello-api", env == "staging", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.WriteService("staging", "other", manifest.Service{Name: "worker", Runtime: "go", Port: 9000}, "reg/urbex/other-worker", false, nil); err != nil {
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

func TestCompose_ExtraEnvironment(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	svc := manifest.Service{Name: "api", Runtime: "go", Port: 8080, Extra: map[string]string{"KEYCLOAK_ISSUER": "https://auth.example/realms/a-staging", "KEYCLOAK_REALM": "a-staging"}}
	if err := repo.WriteService("staging", "a", svc, "reg/a-api", true, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(repo.Path, "environments/staging/a/api/compose.yaml"))
	if !strings.Contains(string(data), `KEYCLOAK_ISSUER: "https://auth.example/realms/a-staging"`) || !strings.Contains(string(data), `KEYCLOAK_REALM: "a-staging"`) {
		t.Errorf("compose lacks the extra environment:\n%s", data)
	}
}

// A project's containers carry the labels the telemetry agent tags their
// logs and metrics with (ADR-0027) - the web frontend's too, which runs
// on the platform's builder.
func TestCompose_KindLabels(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	api := manifest.Service{Name: "api", Runtime: "go", Port: 8080}
	web := manifest.Service{Name: manifest.WebService, Runtime: manifest.WebRuntime}
	for _, svc := range []manifest.Service{api, web} {
		if err := repo.WriteService("staging", "hello", svc, "reg/hello-"+svc.Name, true, nil); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(repo.Path, "environments/staging/hello", svc.Name, "compose.yaml"))
		want := "    labels:\n      urbex.kind: \"app\"\n      urbex.service: \"" + svc.Name + "\"\n      urbex.project: \"hello\"\n      urbex.env: \"staging\"\n"
		if !strings.Contains(string(data), want) {
			t.Errorf("%s's compose lacks the kind labels:\n%s", svc.Name, data)
		}
	}
}

func TestWriteService_ApplicationFromAnImage(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	dir := filepath.Join(repo.Path, "environments", "prod", "photos", "cache")
	svc := manifest.Service{Name: "cache", Image: "valkey/valkey", Version: "9"}

	if err := repo.WriteService("prod", "photos", svc, svc.Image, false, nil); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	compose := read(t, filepath.Join(dir, ComposeFile))
	if !strings.Contains(compose, "image: valkey/valkey:${VERSION}\n") {
		t.Errorf("expected the published image, its tag and digest from version.env:\n%s", compose)
	}
	// It reads its own variables and, without a port, publishes none.
	for _, unwanted := range []string{"PORT", "ports:", "environment:", "healthcheck:"} {
		if strings.Contains(compose, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, compose)
		}
	}

	pinned := "9.0.1@sha256:0123456789abcdef"
	if err := repo.SetVersion("prod", "photos", "cache", VersionFileState{Version: pinned}); err != nil {
		t.Fatal(err)
	}
	version := read(t, filepath.Join(dir, VersionFile))
	if !strings.Contains(version, "VERSION="+pinned+"\n") || !strings.Contains(version, "digest") {
		t.Errorf("unexpected version file:\n%s", version)
	}
	if st, _ := repo.Version("prod", "photos", "cache"); st.Version != pinned || st.Track {
		t.Errorf("Version = %+v", st)
	}

	svc.Port = 6379
	if err := repo.WriteService("prod", "photos", svc, svc.Image, false, nil); err != nil {
		t.Fatal(err)
	}
	if compose := read(t, filepath.Join(dir, ComposeFile)); !strings.Contains(compose, `- "6379:6379"`) || strings.Contains(compose, "PORT") {
		t.Errorf("expected its port published, with no PORT variable:\n%s", compose)
	}
}

// A named volume is a Docker named volume on the folder of its mount
// point, so Docker fills it with the image's files and owners; a share
// is a folder of the node, bound as it is.
func TestCompose_Volumes(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	svc := manifest.Service{Name: "jellyfin", Image: "jellyfin/jellyfin", Version: "10", Port: 8096, Volumes: []manifest.Volume{
		{Name: "config", Path: "/config", Size: "5Gi"},
		{Share: "media", Path: "/media", ReadOnly: true},
	}}
	if err := repo.WriteService("prod", "media", svc, svc.Image, false, nil); err != nil {
		t.Fatal(err)
	}
	compose := read(t, filepath.Join(repo.Path, "environments/prod/media/jellyfin", ComposeFile))
	want := "    volumes:\n      - config:/config\n      - /srv/shares/media:/media:ro\n"
	if !strings.Contains(compose, want) {
		t.Errorf("expected the named volume and the bound share:\n%s", compose)
	}
	top := "volumes:\n  config:\n    driver: local\n    driver_opts:\n      type: none\n      o: bind\n      device: /srv/volumes/jellyfin/config\n"
	if !strings.HasSuffix(compose, top) {
		t.Errorf("expected the volume declared on its mount point, at the end:\n%s", compose)
	}
	if strings.Contains(compose, "media:\n    driver") {
		t.Errorf("a share is not a Docker volume:\n%s", compose)
	}
}

func TestCompose_NoVolumesNoVolumesBlock(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	svc := manifest.Service{Name: "api", Runtime: "go", Port: 8080}
	if err := repo.WriteService("prod", "demo", svc, "reg/demo-api", false, nil); err != nil {
		t.Fatal(err)
	}
	if compose := read(t, filepath.Join(repo.Path, "environments/prod/demo/api", ComposeFile)); strings.Contains(compose, "volumes:") {
		t.Errorf("a service without volumes has none:\n%s", compose)
	}
}

func linked() (manifest.Service, *Links) {
	svc := manifest.Service{
		Name: "api", Runtime: "go", Port: 8080, Uses: []string{"db", "ml", "auth"},
		Env: map[string]map[string]string{"staging": {"LOG_LEVEL": "debug", "DB_PASSWORD": "${db.password}"}},
	}
	return svc, &Links{
		Env: map[string]string{"PGHOST": "db.staging.my-app.lab", "ML_URL": "http://ml.staging.my-app.lab:3003"},
		Waits: []Wait{
			{Name: "db", Interface: manifest.InterfacePostgres, Host: "db.staging.my-app.lab", Port: 5432},
			{Name: "ml", Host: "ml.staging.my-app.lab", Port: 3003, Healthcheck: "/healthz"},
			{Name: "auth", Host: "auth.staging.my-app.lab", Port: 9000, Healthcheck: "/ready"},
		},
		WaitSeconds: 180,
	}
}

func TestWriteService_LinkedServiceWaitsInItsOwnStack(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	svc, links := linked()
	if err := repo.WriteService("staging", "my-app", svc, "img", false, links); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo.Path, "environments", "staging", "my-app", "api")
	compose := read(t, filepath.Join(dir, ComposeFile))
	for _, want := range []string{
		"depends_on:\n      wait:\n        condition: service_completed_successfully",
		"  wait:\n    image: " + WaitImage,
		`restart: "no"`,
		"end=$$(( $$(date +%s) + 180 ))",
		`name=db host=db.staging.my-app.lab; probe() { pg_isready -q -t 2 -h "$$1" -p 5432; }; wait_for || exit 1`,
		`name=ml host=ml.staging.my-app.lab; probe() { wget -q --spider -T 2 --header "Host: ml.staging.my-app.lab:3003" "http://$$1:3003/healthz"; }; wait_for || exit 1`,
		`name=auth host=auth.staging.my-app.lab; probe() { wget -q --spider -T 2 --header "Host: auth.staging.my-app.lab:9000" "http://$$1:9000/ready"; }; wait_for || exit 1`,
		// How it decides is TestWait_*; a provider that doesn't answer
		// is probed less and less often, with jitter.
		"sleep $$((delay + $$RANDOM % (delay + 1)))",
		"[ $$delay -lt 8 ] && delay=$$((delay * 2))",
		"      - urbex.env\n      - path: ${URBEX_GENERATED_SECRETS_FILE:-urbex.secrets.env}\n        required: false",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose is missing %q:\n%s", want, compose)
		}
	}
	// The user's files come first, so the generated ones win.
	if strings.Index(compose, "      - config.env") > strings.Index(compose, "      - urbex.env") {
		t.Error("the generated file must load after the user's")
	}
	gen := read(t, filepath.Join(dir, GeneratedFile))
	if !strings.Contains(gen, "PGHOST=db.staging.my-app.lab\n") || !strings.Contains(gen, "ML_URL=http://") {
		t.Errorf("urbex.env:\n%s", gen)
	}
	if _, err := os.Stat(filepath.Join(dir, GeneratedSecretsFile)); err != nil {
		t.Errorf("the generated secrets file must exist, Komodo tracks it: %v", err)
	}
	// A value that references what urbex generates is not in config.env.
	cfg := read(t, filepath.Join(dir, ConfigFile))
	if !strings.Contains(cfg, "LOG_LEVEL=debug") || strings.Contains(cfg, "DB_PASSWORD") {
		t.Errorf("config.env:\n%s", cfg)
	}
}

func TestWriteService_GeneratedFileIsRewrittenAndTheSecretsOneIsKept(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	svc, links := linked()
	dir := filepath.Join(repo.Path, "environments", "staging", "my-app", "api")
	if err := repo.WriteService("staging", "my-app", svc, "img", false, links); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, GeneratedSecretsFile), []byte("DATABASE_URL=ENC[x]\nsops_version=3\n"), 0o644)
	os.WriteFile(filepath.Join(dir, GeneratedFile), []byte("STALE=1\n"), 0o644)
	if err := repo.WriteService("staging", "my-app", svc, "img", false, links); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, filepath.Join(dir, GeneratedFile)), "STALE") {
		t.Error("urbex.env must be rewritten by every apply")
	}
	if !strings.Contains(read(t, filepath.Join(dir, GeneratedSecretsFile)), "DATABASE_URL=ENC[x]") {
		t.Error("the encrypted generated file is not envrepo's to rewrite")
	}
	keys, err := repo.GeneratedKeys("staging", "my-app", "api")
	if err != nil || strings.Join(keys, ",") != "ML_URL,PGHOST,DATABASE_URL" && strings.Join(keys, ",") != "PGHOST,ML_URL,DATABASE_URL" {
		t.Errorf("GeneratedKeys = %v, %v", keys, err)
	}
}

func TestWriteService_PostgresProviderPublishesAndChecksItself(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	db := manifest.Service{Name: "db", Image: "postgres", Version: "17", Provides: manifest.InterfacePostgres,
		Volumes: []manifest.Volume{{Name: "data", Path: "/var/lib/postgresql"}}}
	if err := repo.WriteService("staging", "my-app", db, "postgres", false, &Links{Env: map[string]string{"POSTGRES_USER": "urbex"}}); err != nil {
		t.Fatal(err)
	}
	compose := read(t, filepath.Join(repo.Path, "environments", "staging", "my-app", "db", ComposeFile))
	for _, want := range []string{
		`- "5432:5432"`,
		`pg_isready -h 127.0.0.1 -U $$POSTGRES_USER -d $$POSTGRES_DB`,
		"- data:/var/lib/postgresql",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose is missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "  wait:") || strings.Contains(compose, "depends_on") {
		t.Errorf("a provider that uses nothing waits for nothing:\n%s", compose)
	}
}

func TestWriteService_AnUnlinkedServiceIsUnchanged(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	if err := repo.WriteService("staging", "my-app", service(), "img", false, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo.Path, "environments", "staging", "my-app", "api")
	if _, err := os.Stat(filepath.Join(dir, GeneratedFile)); !os.IsNotExist(err) {
		t.Error("an unlinked service has no generated file")
	}
	if strings.Contains(read(t, filepath.Join(dir, ComposeFile)), "      - urbex.env") {
		t.Error("an unlinked service does not load the generated file")
	}
}
