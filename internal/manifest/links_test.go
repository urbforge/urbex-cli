package manifest

import (
	"strings"
	"testing"
)

func stack() *Manifest {
	return &Manifest{Project: "acme", Services: []Service{
		{Name: "db", Image: "postgres", Version: "17", Provides: InterfacePostgres, Volumes: []Volume{{Name: "data", Path: "/var/lib/postgresql"}}},
		{Name: "api", Runtime: "go", Port: 8080, Healthcheck: "/healthz", Uses: []string{"db"}},
		{Name: "web", Runtime: "go", Port: 8081, Uses: []string{"api", "db"}},
	}}
}

func TestLinks_AProviderNeedsAWayToTellItIsReady(t *testing.T) {
	for name, api := range map[string]Service{
		"no healthcheck": {Name: "api", Runtime: "go", Port: 8080, Uses: []string{"db"}},
		"no port":        {Name: "api", Runtime: "go", Healthcheck: "/healthz", Uses: []string{"db"}},
	} {
		m := stack()
		m.Services[1] = api
		got := strings.Join(m.Unsupported(), "; ")
		if !strings.Contains(got, "services web: uses api, which has neither an interface nor a healthcheck") {
			t.Errorf("%s: Unsupported = %q", name, got)
		}
	}
}

func TestLinks_AValidStackHasNoErrors(t *testing.T) {
	if got := stack().Unsupported(); len(got) != 0 {
		t.Errorf("Unsupported = %v", got)
	}
}

func TestLinks_UnknownAndSelf(t *testing.T) {
	m := stack()
	m.Services[1].Uses = []string{"api", "nowhere"}
	got := strings.Join(m.Unsupported(), "; ")
	for _, want := range []string{"services api: uses itself", "services api: uses nowhere, which urbex.yaml does not declare"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestLinks_ACycleIsAnError(t *testing.T) {
	m := stack()
	m.Services[0].Uses = []string{"web"}   // db -> web -> api -> db
	m.Services[2].Healthcheck = "/healthz" // so that only the cycle is wrong
	got := strings.Join(m.Unsupported(), "; ")
	if !strings.Contains(got, "uses form a cycle (db -> web -> api -> db)") {
		t.Errorf("Unsupported = %q", got)
	}
}

func TestLinks_Depth(t *testing.T) {
	// The graph of the PoC: dns -> gitea -> keycloak -> photos,
	// gitea -> acme-db, and acme-api using acme-db and keycloak.
	m := &Manifest{Services: []Service{
		{Name: "dns"},
		{Name: "gitea", Uses: []string{"dns"}},
		{Name: "keycloak", Uses: []string{"gitea"}},
		{Name: "photos", Uses: []string{"keycloak"}},
		{Name: "acme-db", Uses: []string{"gitea"}},
		{Name: "acme-api", Uses: []string{"acme-db", "keycloak"}},
	}}
	want := map[string]int{"dns": 0, "gitea": 1, "keycloak": 2, "photos": 3, "acme-db": 2, "acme-api": 3}
	for name, depth := range want {
		if got := m.Depth(name); got != depth {
			t.Errorf("Depth(%s) = %d, want %d", name, got, depth)
		}
	}
	if got := m.WaitSeconds("photos"); got != 3*60 {
		t.Errorf("WaitSeconds(photos) = %d, want the default ready_within of three levels", got)
	}
	if got := m.WaitSeconds("dns"); got != 0 {
		t.Errorf("WaitSeconds(dns) = %d", got)
	}
}

func TestLinks_WaitSecondsSumsTheLongestChainOfReadyWithin(t *testing.T) {
	// web waits for api (90 s) after db (5 m), or for db alone: the
	// longer of the two.
	m := stack()
	m.Services[0].ReadyWithin = "5m"
	m.Services[1].ReadyWithin = "90s"
	if got := m.WaitSeconds("web"); got != 300+90 {
		t.Errorf("WaitSeconds(web) = %d, want 390", got)
	}
	if got := m.WaitSeconds("api"); got != 300 {
		t.Errorf("WaitSeconds(api) = %d, want 300", got)
	}
	for v, want := range map[string]int{"": 60, "45s": 45, "2m": 120} {
		if got := (Service{ReadyWithin: v}).ReadySeconds(); got != want {
			t.Errorf("ReadySeconds(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestLinks_References(t *testing.T) {
	m := stack()
	m.Services[1].Env = map[string]map[string]string{"staging": {
		"DB_HOSTNAME": "${db.host}",
		"DB_PASSWORD": "${db.password}",
		"STRAY":       "${web.url}",
		"BAD":         "${db.shard}",
	}}
	got := strings.Join(m.Unsupported(), "; ")
	for _, want := range []string{
		"env.staging.STRAY references ${web.url}, but api does not use web",
		"env.staging.BAD references ${db.shard}, which db does not export (it exports host, port, url, user, password, database)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "DB_HOSTNAME") || strings.Contains(got, "DB_PASSWORD") {
		t.Errorf("valid references were reported: %q", got)
	}
}

func TestLinks_AnApplicationWithNoInterfaceExportsHostPortURL(t *testing.T) {
	if got := strings.Join(InterfaceKeys(""), ","); got != "host,port,url" {
		t.Errorf("InterfaceKeys(\"\") = %s", got)
	}
}
