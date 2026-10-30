package links

import (
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

func stack() *manifest.Manifest {
	return &manifest.Manifest{Project: "acme-app", Services: []manifest.Service{
		{Name: "db", Image: "postgres", Version: "17", Provides: manifest.InterfacePostgres, Volumes: []manifest.Volume{{Name: "data", Path: "/var/lib/postgresql"}}},
		{Name: "admin-ui", Runtime: "go", Port: 8080, Uses: []string{"db"}},
		{Name: "ml", Image: "ml", Version: "1", Port: 3003},
		{Name: "api", Runtime: "go", Port: 8081, Uses: []string{"db", "ml"}, Env: map[string]map[string]string{
			"staging": {"DB_HOSTNAME": "${db.host}", "DB_PASSWORD": "${db.password}", "ML": "${ml.url}", "PLAIN": "x"},
		}},
	}}
}

func inputs(existing map[string]map[string]string) Inputs {
	n := 0
	return Inputs{
		Host:     func(s manifest.Service) string { return s.Name + ".staging.acme-app.lab" },
		Existing: existing,
		Password: func() string { n++; return strings.Repeat(string(rune('a'+n)), 32) },
	}
}

func TestBuild_ProviderAndUsers(t *testing.T) {
	plan, err := Build(stack(), "staging", inputs(nil))
	if err != nil {
		t.Fatal(err)
	}
	db := plan.Plain["db"]
	if db["POSTGRES_USER"] != "urbex" || db["POSTGRES_DB"] != "acme_app" || db["PGDATA"] != "/var/lib/postgresql/pgdata" {
		t.Errorf("provider plain = %v", db)
	}
	if len(plan.Secret["db"]["POSTGRES_PASSWORD"]) != 32 {
		t.Errorf("provider secret = %v", plan.Secret["db"])
	}
	ui, api := plan.Plain["admin-ui"], plan.Plain["api"]
	if ui["PGHOST"] != "db.staging.acme-app.lab" || ui["PGPORT"] != "5432" || ui["PGDATABASE"] != "acme_app" || ui["PGUSER"] != "admin_ui" {
		t.Errorf("admin-ui plain = %v", ui)
	}
	if api["PGUSER"] != "api" {
		t.Errorf("api gets its own role, got %v", api)
	}
	pw := plan.Secret["admin-ui"]["PGPASSWORD"]
	if want := "postgres://admin_ui:" + pw + "@db.staging.acme-app.lab:5432/acme_app"; plan.Secret["admin-ui"]["DATABASE_URL"] != want {
		t.Errorf("DATABASE_URL = %q, want %q", plan.Secret["admin-ui"]["DATABASE_URL"], want)
	}
	if plan.Secret["api"]["PGPASSWORD"] == pw || pw == plan.Secret["db"]["POSTGRES_PASSWORD"] {
		t.Error("each application and the server have their own password")
	}
	if strings.Contains(strings.Join(mapValues(plan.Plain["admin-ui"]), " "), pw) {
		t.Error("a password is in the plain variables")
	}
}

func TestBuild_AnApplicationWithNoInterface(t *testing.T) {
	plan, _ := Build(stack(), "staging", inputs(nil))
	api := plan.Plain["api"]
	if api["ML_HOST"] != "ml.staging.acme-app.lab" || api["ML_PORT"] != "3003" || api["ML_URL"] != "http://ml.staging.acme-app.lab:3003" {
		t.Errorf("api plain = %v", api)
	}
}

func TestBuild_ReferencesInEnv(t *testing.T) {
	plan, err := Build(stack(), "staging", inputs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Plain["api"]["DB_HOSTNAME"] != "db.staging.acme-app.lab" || plan.Plain["api"]["ML"] != "http://ml.staging.acme-app.lab:3003" {
		t.Errorf("api plain = %v", plan.Plain["api"])
	}
	if plan.Secret["api"]["DB_PASSWORD"] != plan.Secret["api"]["PGPASSWORD"] {
		t.Error("a reference to password is the application's role password, and a secret")
	}
	if _, ok := plan.Plain["api"]["DB_PASSWORD"]; ok {
		t.Error("a password is in the plain variables")
	}
	if _, ok := plan.Plain["api"]["PLAIN"]; ok {
		t.Error("a value with no reference stays in config.env")
	}
}

func TestBuild_PasswordsAreGeneratedOnce(t *testing.T) {
	first, _ := Build(stack(), "staging", inputs(nil))
	second, _ := Build(stack(), "staging", inputs(first.Secret))
	for app, secrets := range first.Secret {
		for k, v := range secrets {
			if second.Secret[app][k] != v {
				t.Errorf("%s %s changed between two builds", app, k)
			}
		}
	}
}

func TestBuild_TwoProvidersOfAnInterfaceArePrefixed(t *testing.T) {
	m := stack()
	m.Services = append(m.Services, manifest.Service{Name: "analytics", Image: "postgres", Version: "17", Provides: manifest.InterfacePostgres, Volumes: []manifest.Volume{{Name: "data", Path: "/var/lib/postgresql"}}})
	m.Services[1].Uses = []string{"db", "analytics"}
	plan, _ := Build(m, "staging", inputs(nil))
	ui := plan.Secret["admin-ui"]
	if ui["DB_DATABASE_URL"] == "" || ui["ANALYTICS_DATABASE_URL"] == "" || ui["DATABASE_URL"] != "" {
		t.Errorf("admin-ui secrets = %v", ui)
	}
	if plan.Plain["admin-ui"]["ANALYTICS_PGHOST"] != "analytics.staging.acme-app.lab" {
		t.Errorf("admin-ui plain = %v", plan.Plain["admin-ui"])
	}
}

func TestBuild_AnUnknownReferenceIsAnError(t *testing.T) {
	m := stack()
	m.Services[3].Env["staging"]["BAD"] = "${db.shard}"
	if _, err := Build(m, "staging", inputs(nil)); err == nil || !strings.Contains(err.Error(), "db does not export shard") {
		t.Errorf("got %v", err)
	}
}

func TestNewPassword(t *testing.T) {
	a, b := NewPassword(), NewPassword()
	if len(a) != 32 || a == b {
		t.Errorf("passwords = %q, %q", a, b)
	}
	for _, r := range a {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			t.Errorf("%q has %q, which needs escaping in a URL", a, r)
		}
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
