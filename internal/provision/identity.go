package provision

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/keycloak"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/secrets"
)

// KeycloakAPI is the subset of keycloak.Client the orchestrator uses to
// keep a project environment's realm (ADR-0024).
type KeycloakAPI interface {
	WaitReady(ctx context.Context, timeout time.Duration, waiting func()) error
	EnsureRealm(ctx context.Context, realm, displayName string) error
	EnsureRoles(ctx context.Context, realm string, roles []string) error
	EnsurePublicClient(ctx context.Context, realm string, pc keycloak.PublicClient) error
	EnsureUser(ctx context.Context, realm string, u keycloak.TestUser) error
	DeleteRealm(ctx context.Context, realm string) error
}

// SecretStore reads and writes SOPS-encrypted dotenv files (secrets.Store).
type SecretStore interface {
	Read(ctx context.Context, path string) (map[string]string, error)
	Write(ctx context.Context, path string, values map[string]string) error
}

func (o *Orchestrator) secretStore() (SecretStore, error) {
	if o.Secrets != nil {
		return o.Secrets, nil
	}
	return secrets.NewStore(o.Credentials.AgeKey)
}

// TestClient is the client, in staging realms only, that lets test users
// log in with their password (e.g. with curl) to call the APIs.
const TestClient = "urbex-test"

// TestUsersFile holds the staging test users' passwords, SOPS-encrypted,
// in the project's staging folder of the GitOps repo.
const TestUsersFile = "keycloak-users.sops.env"

// Realm is the project's Keycloak realm in an environment:
// <project>-<env>. Environments don't share users.
func (o *Orchestrator) Realm(env string) string { return o.Manifest.Project + "-" + env }

// Issuer is the realm's OIDC issuer: Keycloak's public address with
// Cloudflare, its LAN one otherwise.
func (o *Orchestrator) Issuer(env string) string {
	base := o.KeycloakURL
	if o.Config.Cloudflare.Enabled() {
		base = "https://" + o.Config.Cloudflare.Hostname(o.Config.Domain, "auth")
	}
	return base + "/realms/" + o.Realm(env)
}

// keycloakEnv is what a service with auth.keycloak gets to validate
// tokens: the issuer they carry, and where the signing keys are (by
// internal name).
func (o *Orchestrator) keycloakEnv(env string) map[string]string {
	if o.KeycloakURL == "" {
		return nil
	}
	return map[string]string{
		"KEYCLOAK_REALM":    o.Realm(env),
		"KEYCLOAK_ISSUER":   o.Issuer(env),
		"KEYCLOAK_JWKS_URL": o.KeycloakInternalURL + "/realms/" + o.Realm(env) + "/protocol/openid-connect/certs",
	}
}

// Roles are the realm roles the services ask for (auth.roles of the
// services with auth.keycloak: true).
func (o *Orchestrator) Roles() []string {
	seen := map[string]bool{}
	var out []string
	for _, svc := range o.Manifest.Services {
		if svc.Auth == nil || !svc.Auth.Keycloak {
			continue
		}
		for _, r := range svc.Auth.Roles {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestUsername is the staging test user that has a role.
func TestUsername(role string) string { return "test-" + role }

var nonKey = regexp.MustCompile(`[^A-Za-z0-9_]`)

// testUserKey is where a test user's password is kept in TestUsersFile.
func testUserKey(role string) string {
	return "TEST_" + strings.ToUpper(nonKey.ReplaceAllString(role, "_"))
}

// frontendClient is the OIDC client of the frontend in an environment:
// "web" for a web frontend, served at its public hostname; "mobile" for a
// mobile app, redirecting to its custom scheme (<project>://...).
func (o *Orchestrator) frontendClient(env string) (keycloak.PublicClient, bool) {
	f := o.Manifest.Frontend
	if f == nil {
		return keycloak.PublicClient{}, false
	}
	switch f.Type {
	case "web":
		host := o.PublicHostname(env, manifest.Service{Name: manifest.WebService, Runtime: manifest.WebRuntime})
		if host == "" {
			return keycloak.PublicClient{}, false
		}
		return keycloak.PublicClient{ClientID: "web", Name: o.Manifest.Project + " web",
			RedirectURIs: []string{"https://" + host + "/*"}, WebOrigins: []string{"https://" + host}}, true
	case "mobile":
		return keycloak.PublicClient{ClientID: "mobile", Name: o.Manifest.Project + " mobile",
			RedirectURIs: []string{o.Manifest.Project + "://*"}}, true
	}
	return keycloak.PublicClient{}, false
}

// setupIdentity keeps the environment's realm: the realm, the roles the
// services declare, a client per frontend and - in staging - a user per
// role, whose passwords it keeps encrypted in the GitOps repo. It reports
// whether that file changed.
func (o *Orchestrator) setupIdentity(ctx context.Context, opts Options) (bool, error) {
	if o.Keycloak == nil {
		return false, nil
	}
	env, realm := opts.Env, o.Realm(opts.Env)
	if err := o.waitKeycloak(ctx); err != nil {
		return false, err
	}
	if err := o.Keycloak.EnsureRealm(ctx, realm, o.Manifest.Project+" ("+env+")"); err != nil {
		return false, err
	}
	roles := o.Roles()
	if err := o.Keycloak.EnsureRoles(ctx, realm, roles); err != nil {
		return false, err
	}
	var clients []string
	if pc, ok := o.frontendClient(env); ok {
		if err := o.Keycloak.EnsurePublicClient(ctx, realm, pc); err != nil {
			return false, err
		}
		clients = append(clients, pc.ClientID)
	}
	changed := false
	if env == "staging" && len(roles) > 0 {
		if err := o.Keycloak.EnsurePublicClient(ctx, realm, keycloak.PublicClient{ClientID: TestClient, Name: "urbex test users", DirectAccessGrants: true}); err != nil {
			return false, err
		}
		clients = append(clients, TestClient)
		var err error
		if changed, err = o.ensureTestUsers(ctx, opts, roles); err != nil {
			return false, err
		}
	}
	fmt.Fprintf(o.Out, "Keycloak: realm %s (issuer %s)", realm, o.Issuer(env))
	if len(clients) > 0 {
		fmt.Fprintf(o.Out, ", clients %s", strings.Join(clients, ", "))
	}
	fmt.Fprintln(o.Out, ".")
	if env == "staging" && len(roles) > 0 {
		fmt.Fprintf(o.Out, "Test users, one per role: %s - 'urbex users staging' shows their passwords.\n", strings.Join(testUsernames(roles), ", "))
	}
	return changed, nil
}

func testUsernames(roles []string) []string {
	var out []string
	for _, r := range roles {
		out = append(out, TestUsername(r))
	}
	return out
}

// ensureTestUsers creates a staging user per role, reusing the passwords
// kept in the GitOps repo and generating the missing ones.
func (o *Orchestrator) ensureTestUsers(ctx context.Context, opts Options, roles []string) (bool, error) {
	store, err := o.secretStore()
	if err != nil {
		return false, err
	}
	path := testUsersPath(opts.GitOpsRepoPath, opts.Env, o.Manifest.Project)
	passwords, err := store.Read(ctx, path)
	if err != nil {
		return false, err
	}
	changed := false
	for _, role := range roles {
		key := testUserKey(role)
		if passwords[key] == "" {
			p, err := credentials.RandomSecret()
			if err != nil {
				return false, err
			}
			passwords[key] = p[:24]
			changed = true
		}
		u := keycloak.TestUser{Username: TestUsername(role), Password: passwords[key],
			Email: TestUsername(role) + "@" + o.Manifest.Project + ".test", FirstName: "Test", LastName: role, Roles: []string{role}}
		if err := o.Keycloak.EnsureUser(ctx, o.Realm(opts.Env), u); err != nil {
			return false, err
		}
	}
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		if err := store.Write(ctx, path, passwords); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func testUsersPath(repoPath, env, project string) string {
	return filepath.Join(repoPath, filepath.FromSlash(envrepo.ProjectDir(env, project)), TestUsersFile)
}

// TestUsers returns the staging test users and their passwords, decrypted
// from the GitOps repo.
func (o *Orchestrator) TestUsers(ctx context.Context, repoPath, env string) (map[string]string, error) {
	store, err := o.secretStore()
	if err != nil {
		return nil, err
	}
	passwords, err := store.Read(ctx, testUsersPath(repoPath, env, o.Manifest.Project))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, role := range o.Roles() {
		if p := passwords[testUserKey(role)]; p != "" {
			out[TestUsername(role)] = p
		}
	}
	return out, nil
}

// waitKeycloak waits for Keycloak to answer before using it: right after
// a bootstrap, its first start can take minutes (urbforge/urbex-cli#32).
func (o *Orchestrator) waitKeycloak(ctx context.Context) error {
	return o.Keycloak.WaitReady(ctx, keycloak.StartTimeout, func() {
		fmt.Fprintln(o.Out, "Waiting for Keycloak to start - its first start takes a few minutes...")
	})
}

// teardownIdentity deletes the environment's realm, with its users.
func (o *Orchestrator) teardownIdentity(ctx context.Context, env string) error {
	if o.Keycloak == nil {
		return nil
	}
	if err := o.waitKeycloak(ctx); err != nil {
		return err
	}
	if err := o.Keycloak.DeleteRealm(ctx, o.Realm(env)); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Deleted Keycloak realm %s (its users and clients).\n", o.Realm(env))
	return nil
}
