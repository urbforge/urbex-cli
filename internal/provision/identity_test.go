package provision

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/keycloak"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type fakeKeycloak struct {
	notReady bool // WaitReady says Keycloak never answered
	waited   int
	realms   map[string]bool
	roles    map[string][]string
	clients  map[string]keycloak.PublicClient // realm/clientId
	users    map[string]keycloak.TestUser     // realm/username
}

func (f *fakeKeycloak) init() {
	if f.realms == nil {
		f.realms, f.roles = map[string]bool{}, map[string][]string{}
		f.clients, f.users = map[string]keycloak.PublicClient{}, map[string]keycloak.TestUser{}
	}
}
func (f *fakeKeycloak) WaitReady(_ context.Context, _ time.Duration, waiting func()) error {
	f.waited++
	if f.notReady {
		waiting()
		return errors.New("keycloak at http://kc didn't answer within 15m0s")
	}
	return nil
}
func (f *fakeKeycloak) EnsureRealm(_ context.Context, realm, _ string) error {
	f.init()
	f.realms[realm] = true
	return nil
}
func (f *fakeKeycloak) EnsureRoles(_ context.Context, realm string, roles []string) error {
	f.roles[realm] = roles
	return nil
}
func (f *fakeKeycloak) EnsurePublicClient(_ context.Context, realm string, pc keycloak.PublicClient) error {
	f.clients[realm+"/"+pc.ClientID] = pc
	return nil
}
func (f *fakeKeycloak) EnsureUser(_ context.Context, realm string, u keycloak.TestUser) error {
	f.users[realm+"/"+u.Username] = u
	return nil
}
func (f *fakeKeycloak) DeleteRealm(_ context.Context, realm string) error {
	f.init()
	delete(f.realms, realm)
	return nil
}

type memStore map[string]map[string]string

func (m memStore) Read(_ context.Context, path string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m[path] {
		out[k] = v
	}
	return out, nil
}
func (m memStore) Write(_ context.Context, path string, values map[string]string) error {
	m[path] = values
	return nil
}

// A project gets a realm per environment, with the roles its services
// declare and a client per frontend; staging also gets a user per role,
// whose passwords are kept (encrypted) and reused. Services with
// auth.keycloak learn their realm's issuer. Destroy deletes the realm.
func TestIdentity_RealmPerEnvironmentWithTestUsersInStaging(t *testing.T) {
	o, _, _ := cloudflareOrchestrator(t)
	kc, store := &fakeKeycloak{}, memStore{}
	o.Keycloak, o.KeycloakURL, o.KeycloakInternalURL, o.Secrets = kc, "http://192.168.1.203:8080", "http://auth.urbex.cubotto.net:8080", store
	o.Manifest.Frontend = &manifest.Frontend{Type: "web", Provider: "cloudflare-pages", BuildCommand: "b", OutputDir: "d"}
	o.Manifest.Services[0].Auth = &manifest.Auth{Keycloak: true, Roles: []string{"member", "admin"}}
	dir := t.TempDir()
	for _, env := range []string{"staging", "prod"} {
		if err := o.Apply(context.Background(), Options{Env: env, GitOpsRepoPath: dir}); err != nil {
			t.Fatalf("Apply %s: %v", env, err)
		}
	}
	if !kc.realms["my-app-staging"] || !kc.realms["my-app-prod"] || strings.Join(kc.roles["my-app-prod"], ",") != "admin,member" {
		t.Errorf("realms %v roles %v", kc.realms, kc.roles)
	}
	if web := kc.clients["my-app-prod/web"]; strings.Join(web.RedirectURIs, ",") != "https://my-app-urbex.cubotto.net/*" {
		t.Errorf("prod web client = %+v", web)
	}
	if web := kc.clients["my-app-staging/web"]; strings.Join(web.RedirectURIs, ",") != "https://staging-my-app-urbex.cubotto.net/*" {
		t.Errorf("staging web client = %+v", web)
	}
	if !kc.clients["my-app-staging/"+TestClient].DirectAccessGrants {
		t.Error("staging needs the test client, with password logins")
	}
	if _, ok := kc.clients["my-app-prod/"+TestClient]; ok {
		t.Error("prod must have no test client")
	}
	member := kc.users["my-app-staging/test-member"]
	if member.Password == "" || strings.Join(member.Roles, ",") != "member" || len(kc.users) != 2 {
		t.Errorf("staging test users = %+v", kc.users)
	}
	for k := range kc.users {
		if strings.HasPrefix(k, "my-app-prod/") {
			t.Errorf("no test user in prod, found %s", k)
		}
	}

	// Re-applying keeps the passwords.
	before := member.Password
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatal(err)
	}
	if kc.users["my-app-staging/test-member"].Password != before {
		t.Error("a test user's password must not change on re-apply")
	}
	users, err := o.TestUsers(context.Background(), dir, "staging")
	if err != nil || users["test-member"] != before || users["test-admin"] == "" {
		t.Errorf("TestUsers = %v, %v", users, err)
	}
	out := o.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "realm my-app-staging (issuer https://auth-urbex.cubotto.net/realms/my-app-staging)") {
		t.Errorf("apply should say the realm and issuer:\n%s", out)
	}

	// The service validating tokens learns its issuer and keys.
	api := o.target("staging").Services[0]
	if api.Extra["KEYCLOAK_ISSUER"] != "https://auth-urbex.cubotto.net/realms/my-app-staging" ||
		api.Extra["KEYCLOAK_JWKS_URL"] != "http://auth.urbex.cubotto.net:8080/realms/my-app-staging/protocol/openid-connect/certs" {
		t.Errorf("api env = %v", api.Extra)
	}

	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatal(err)
	}
	if kc.realms["my-app-staging"] || !kc.realms["my-app-prod"] {
		t.Errorf("destroying staging deletes its realm only: %v", kc.realms)
	}
}

// Right after a bootstrap Keycloak can take minutes to start: apply waits
// for it, says why, and stops with Keycloak's error if it never answers
// (urbforge/urbex-cli#32).
func TestIdentity_ApplyWaitsForKeycloak(t *testing.T) {
	o, _, _ := cloudflareOrchestrator(t)
	kc := &fakeKeycloak{}
	o.Keycloak, o.KeycloakURL, o.Secrets = kc, "http://192.168.1.203:8080", memStore{}
	o.Manifest.Services[0].Auth = &manifest.Auth{Keycloak: true, Roles: []string{"member"}}
	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if kc.waited == 0 || !kc.realms["my-app-prod"] {
		t.Errorf("apply should wait for Keycloak, then set up the realm: waited %d, realms %v", kc.waited, kc.realms)
	}

	o, _, _ = cloudflareOrchestrator(t)
	out := &bytes.Buffer{}
	kc = &fakeKeycloak{notReady: true}
	o.Keycloak, o.KeycloakURL, o.Secrets, o.Out = kc, "http://192.168.1.203:8080", memStore{}, out
	o.Manifest.Services[0].Auth = &manifest.Auth{Keycloak: true, Roles: []string{"member"}}
	err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "didn't answer within") {
		t.Fatalf("Apply = %v, want Keycloak's timeout", err)
	}
	if !strings.Contains(out.String(), "Waiting for Keycloak to start") {
		t.Errorf("apply should say it waits for Keycloak:\n%s", out)
	}
	if len(kc.realms) != 0 {
		t.Errorf("nothing should be set up in a Keycloak that isn't there: %v", kc.realms)
	}
}
