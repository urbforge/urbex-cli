package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

func linkedManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: "urbex/v1",
		Project:    "my-app",
		Services: []manifest.Service{
			{Name: "api", Runtime: "go", Port: 8080, Uses: []string{"db"}, Env: map[string]map[string]string{
				"staging": {"DB_HOSTNAME": "${db.host}", "DB_PASSWORD": "${db.password}"},
			}},
			{Name: "db", Image: "postgres", Version: "17", Provides: manifest.InterfacePostgres,
				Volumes: []manifest.Volume{{Name: "data", Path: "/var/lib/postgresql"}}},
		},
	}
}

func apply(t *testing.T, o *Orchestrator, dir string) {
	t.Helper()
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestApply_LinksGenerateCredentialsOnceAndWaitInTheConsumersStack(t *testing.T) {
	dir := t.TempDir()
	o, _, _, _, _ := imagesOrchestrator(linkedManifest())
	store := memStore{}
	o.Secrets, o.Volumes = store, newFakeVolumes()
	apply(t, o, dir)

	repo := envrepo.Repo{Path: dir}
	dbSecrets := store[repo.GeneratedSecretsPath("staging", "my-app", "db")]
	apiSecrets := store[repo.GeneratedSecretsPath("staging", "my-app", "api")]
	if len(dbSecrets["POSTGRES_PASSWORD"]) != 32 {
		t.Errorf("the server's password is generated, got %v", dbSecrets)
	}
	if apiSecrets["PGPASSWORD"] == "" || apiSecrets["DB_PASSWORD"] != apiSecrets["PGPASSWORD"] ||
		!strings.HasPrefix(apiSecrets["DATABASE_URL"], "postgres://api:"+apiSecrets["PGPASSWORD"]+"@db.staging.my-app.") {
		t.Errorf("api secrets = %v", apiSecrets)
	}

	read := func(service, file string) string {
		data, err := os.ReadFile(filepath.Join(dir, "environments", "staging", "my-app", service, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if env := read("api", envrepo.GeneratedFile); !strings.Contains(env, "PGHOST=db.staging.my-app.") || !strings.Contains(env, "DB_HOSTNAME=db.staging.my-app.") ||
		strings.Contains(env, apiSecrets["PGPASSWORD"]) {
		t.Errorf("api urbex.env:\n%s", env)
	}
	if compose := read("api", envrepo.ComposeFile); !strings.Contains(compose, "wait_for pg_isready -q -t 2 -h db.staging.my-app.") ||
		!strings.Contains(compose, "+ 60 ))") {
		t.Errorf("api compose:\n%s", compose)
	}
	if env := read("db", envrepo.GeneratedFile); !strings.Contains(env, "POSTGRES_DB=my_app") || !strings.Contains(env, "PGDATA=/var/lib/postgresql/pgdata") {
		t.Errorf("db urbex.env:\n%s", env)
	}
	if compose := read("db", envrepo.ComposeFile); strings.Contains(compose, "wait_for") {
		t.Errorf("the provider uses nothing:\n%s", compose)
	}

	// A second apply keeps every password.
	before := map[string]string{"server": dbSecrets["POSTGRES_PASSWORD"], "api": apiSecrets["PGPASSWORD"]}
	apply(t, o, dir)
	dbAfter := store[repo.GeneratedSecretsPath("staging", "my-app", "db")]
	apiAfter := store[repo.GeneratedSecretsPath("staging", "my-app", "api")]
	if dbAfter["POSTGRES_PASSWORD"] != before["server"] || apiAfter["PGPASSWORD"] != before["api"] {
		t.Error("re-applying changed a generated password")
	}
}

func TestApply_AStackWithoutLinksNeedsNoSecretStore(t *testing.T) {
	dir := t.TempDir()
	o, _, _, _, _ := imagesOrchestrator(stackManifest())
	apply(t, o, dir)
	if _, err := os.Stat(filepath.Join(dir, "environments", "staging", "my-app", "api", envrepo.GeneratedFile)); !os.IsNotExist(err) {
		t.Error("a service without links has no generated file")
	}
}

func TestApply_RefusesACycle(t *testing.T) {
	m := linkedManifest()
	m.Services[1].Uses = []string{"api"}
	m.Services[0].Healthcheck = "/healthz" // so that only the cycle is wrong
	o, _, _, _, _ := imagesOrchestrator(m)
	o.Secrets, o.Volumes = memStore{}, newFakeVolumes()
	err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "uses form a cycle (api -> db -> api)") {
		t.Errorf("expected a cycle refused, got %v", err)
	}
}
