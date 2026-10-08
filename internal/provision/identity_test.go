package provision

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/keycloak"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type fakeKeycloak struct {
	realms  map[string]bool
	roles   map[string][]string
	clients map[string]keycloak.PublicClient // realm/clientId
	users   map[string]keycloak.TestUser     // realm/username
}

func (f *fakeKeycloak) init() {
	if f.realms == nil {
		f.realms, f.roles = map[string]bool{}, map[string][]string{}
		f.clients, f.users = map[string]keycloak.PublicClient{}, map[string]keycloak.TestUser{}
	}
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
