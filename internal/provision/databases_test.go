package provision

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/pgadmin"
)

// fakePG is a postgres provider's container as pgadmin sees it through
// its shell: it keeps the roles (with their verifier) and their schemas
// the SQL makes, and says what it changed as the server would.
type fakePG struct {
	roles     map[string]string // role -> verifier
	sql       string            // the script being sent
	scripts   []string
	commands  []string
	notReady  int    // how many times pg_isready fails first
	version   string // PG_VERSION, "" for none
	closed    int
	isolation bool
	// unreachable is a container Komodo can't run a command in.
	unreachable bool
}

func newFakePG() *fakePG { return &fakePG{roles: map[string]string{}, version: "17"} }

var (
	roleRE     = regexp.MustCompile(`CREATE ROLE "([a-z_0-9]+)" LOGIN;`)
	verifierRE = regexp.MustCompile(`ALTER ROLE "([a-z_0-9]+)" LOGIN PASSWORD '([^']+)'`)
	dropRE     = regexp.MustCompile(`DROP ROLE "([a-z_0-9]+)"`)
)

func (f *fakePG) Run(ctx context.Context, command string) (string, int, error) {
	f.commands = append(f.commands, command)
	if f.unreachable {
		return "", 0, errors.New("komodo could not run a command in my-app-db-staging: 400 Bad Request: container not running")
	}
	switch {
	case strings.HasPrefix(command, "pg_isready"):
		if f.notReady > 0 {
			f.notReady--
			return "", 2, nil
		}
		return "", 0, nil
	case strings.Contains(command, "PG_VERSION"):
		if f.version == "" {
			return "none", 0, nil
		}
		return f.version + "\n", 0, nil
	case strings.HasPrefix(command, `URBEX_SQL="$URBEX_SQL"`):
		f.sql += strings.TrimPrefix(command, `URBEX_SQL="$URBEX_SQL"`)
		return "", 0, nil
	case strings.HasPrefix(command, "URBEX_SQL="):
		f.sql = strings.TrimPrefix(command, "URBEX_SQL=")
		return "", 0, nil
	case strings.Contains(command, "base64 -d | "):
		data, err := base64.StdEncoding.DecodeString(f.sql)
		if err != nil {
			return "bad base64", 1, nil
		}
		return f.exec(string(data)), 0, nil
	}
	return "unknown command", 127, nil
}

func (f *fakePG) exec(sql string) string {
	f.scripts = append(f.scripts, sql)
	var out []string
	notice := func(s string) { out = append(out, "psql:<stdin>:1: NOTICE:  urbex: "+s) }
	if strings.HasPrefix(sql, "SELECT r.rolname") {
		var names []string
		for r := range f.roles {
			names = append(names, r)
		}
		sort.Strings(names)
		for _, r := range names {
			out = append(out, r+"|"+r)
		}
		return strings.Join(out, "\n")
	}
	if strings.Contains(sql, "REVOKE ALL ON SCHEMA public") && !f.isolation {
		f.isolation = true
		notice("took the rights on public from everyone")
	}
	for _, m := range roleRE.FindAllStringSubmatch(sql, -1) {
		v := verifierRE.FindStringSubmatch(sql[strings.Index(sql, m[0]):])
		if _, ok := f.roles[m[1]]; !ok {
			f.roles[m[1]] = ""
			notice("created the role " + m[1])
			notice("created the schema " + m[1])
			notice("set the search_path of " + m[1])
		}
		if f.roles[m[1]] != v[2] {
			f.roles[m[1]] = v[2]
			notice("set the password of " + m[1])
		}
	}
	for _, m := range dropRE.FindAllStringSubmatch(sql, -1) {
		delete(f.roles, m[1])
	}
	return strings.Join(out, "\n")
}

func (f *fakePG) Close(ctx context.Context) error { f.closed++; return nil }

// Two applications on one PostgreSQL: apply deploys it, makes their roles
// and schemas once it takes connections, then deploys them - whose waits
// log in with those roles.
func TestApply_MakesTheRolesOfTheApplicationsBeforeDeployingThem(t *testing.T) {
	m := linkedManifest()
	m.Services[0].Healthcheck = "/healthz"
	m.Services = append(m.Services, manifest.Service{Name: "admin-ui", Image: "dpage/pgadmin4", Version: "9", Port: 80, Uses: []string{"db"}})
	dir := t.TempDir()
	o, ro, _, out, _ := imagesOrchestrator(m)
	store := memStore{}
	o.Secrets, o.Volumes = store, newFakeVolumes()
	serverReadyPoll = 0
	ro.pg = map[string]*fakePG{"db": newFakePG()}
	db := ro.pg["db"]
	db.notReady = 2
	apply(t, o, dir)

	if len(ro.deployOrder) == 0 || ro.deployOrder[0] != "db" {
		t.Errorf("db is deployed first, alone: %v", ro.deployOrder)
	}
	repo := envrepo.Repo{Path: dir}
	for _, app := range []string{"api", "admin_ui"} {
		svc := map[string]string{"api": "api", "admin_ui": "admin-ui"}[app]
		password := store[repo.GeneratedSecretsPath("staging", "my-app", svc)]["PGPASSWORD"]
		if db.roles[app] == "" || db.roles[app] != pgadmin.Verifier(app, password) {
			t.Errorf("%s's role should have its password's verifier, has %q", app, db.roles[app])
		}
	}
	for _, s := range db.scripts {
		for _, secret := range store[repo.GeneratedSecretsPath("staging", "my-app", "api")] {
			if strings.Contains(s, secret) {
				t.Errorf("a password went to the server in clear:\n%s", s)
			}
		}
	}
	for _, c := range db.commands {
		if len(c) > 700 {
			t.Errorf("a command of %d bytes is too long for a terminal's line", len(c))
		}
	}
	if !strings.Contains(out.String(), "db: created the role api.") || !strings.Contains(out.String(), "db: took the rights on public from everyone.") {
		t.Errorf("apply should say what it made:\n%s", out)
	}
	if db.closed == 0 {
		t.Error("the shell is left open")
	}
	compose := readFile(t, dir, "environments/staging/my-app/api/compose.yaml")
	if !strings.Contains(compose, `PGPASSWORD="$$URBEX_PASSWORD_DB" PGCONNECT_TIMEOUT=2 psql -X -w -q -A -t -h "$$1" -p 5432 -U api -d my_app -c 'select 1'`) {
		t.Errorf("api's wait should log in with its role:\n%s", compose)
	}

	// A second apply changes nothing on the server.
	out.Reset()
	o2, ro2, _, out2, _ := imagesOrchestrator(m)
	o2.Secrets, o2.Volumes = store, newFakeVolumes()
	ro2.pg = ro.pg
	apply(t, o2, dir)
	if regexp.MustCompile(`db: (created|set|took|gave)`).MatchString(out2.String()) {
		t.Errorf("a second apply should change nothing on db:\n%s", out2)
	}

	// A lost password: a new one, set on the role.
	delete(store, repo.GeneratedSecretsPath("staging", "my-app", "api"))
	o3, ro3, _, out3, _ := imagesOrchestrator(m)
	o3.Secrets, o3.Volumes = store, newFakeVolumes()
	ro3.pg = ro.pg
	apply(t, o3, dir)
	if !strings.Contains(out3.String(), "db: set the password of api.") {
		t.Errorf("a lost password should be set again:\n%s", out3)
	}

	// admin-ui removed: its role and schema stay until --delete-data.
	m.Services = m.Services[:2]
	o4, ro4, _, out4, _ := imagesOrchestrator(m)
	o4.Secrets, o4.Volumes = store, newFakeVolumes()
	ro4.pg = ro.pg
	apply(t, o4, dir)
	if db.roles["admin_ui"] == "" || !strings.Contains(out4.String(), "no application uses the schema and role admin_ui any more: kept") {
		t.Errorf("an unused role should be kept and named:\n%s", out4)
	}
	o5, ro5, _, out5, _ := imagesOrchestrator(m)
	o5.Secrets, o5.Volumes = store, newFakeVolumes()
	ro5.pg = ro.pg
	if err := o5.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, DeleteData: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.roles["admin_ui"]; ok || !strings.Contains(out5.String(), "dropping the schema and role admin_ui") {
		t.Errorf("--delete-data should drop it, naming it first:\n%s", out5)
	}
	if _, ok := db.roles["api"]; !ok {
		t.Error("--delete-data dropped a role in use")
	}
}

// A PostgreSQL's new major is refused, --allow-major or not, while its
// data directory is another major's.
func TestCheckMajor_PostgresFollowsItsDataDirectory(t *testing.T) {
	m := linkedManifest()
	ro := &fakeRollout{pg: map[string]*fakePG{"db": newFakePG()}}
	o := &Orchestrator{Manifest: m, Rollout: ro}
	db := m.Services[1]
	err := o.checkMajor(context.Background(), "staging", db, "17", "18", true)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL 17") || !strings.Contains(err.Error(), "pg_dumpall") {
		t.Errorf("18 on a 17 data directory should be refused, got %v", err)
	}
	ro.pg["db"].version = ""
	if err := o.checkMajor(context.Background(), "staging", db, "17", "18", true); err != nil {
		t.Errorf("an empty data directory takes any major: %v", err)
	}
	ro.pg["db"].version = "18"
	if err := o.checkMajor(context.Background(), "staging", db, "17", "18", true); err != nil {
		t.Errorf("a data directory restored at 18 takes 18: %v", err)
	}
}

func readFile(t *testing.T, dir, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A provider that can't be read - stopped for the manual upgrade - is
// deployed with a warning: PostgreSQL itself refuses another major's data.
func TestCheckMajor_PostgresNotRunningWarns(t *testing.T) {
	m := linkedManifest()
	ro := &fakeRollout{pg: map[string]*fakePG{"db": newFakePG()}}
	ro.pg["db"].unreachable = true
	var out strings.Builder
	o := &Orchestrator{Manifest: m, Rollout: ro, Out: &out}
	if err := o.checkMajor(context.Background(), "staging", m.Services[1], "17", "18", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "can't read its data directory's major version") {
		t.Errorf("expected a warning, got %q", out.String())
	}
}
