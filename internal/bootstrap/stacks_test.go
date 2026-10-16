package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keycloak runs in production mode on the PostgreSQL of its Stack
// (urbex-cli#36): its configuration points it there, the database's
// password is generated once and is only in the encrypted secrets.
func TestRun_KeycloakOnItsOwnPostgreSQL(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	password := o.Credentials.KeycloakDBPassword
	if password == "" {
		t.Fatal("no Keycloak database password generated")
	}
	config, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/config.env"))
	for _, want := range []string{
		"KC_DB=postgres\n", "KC_DB_URL=jdbc:postgresql://postgres:5432/keycloak\n",
		"KC_DB_USERNAME=keycloak\n", "POSTGRES_USER=keycloak\n", "POSTGRES_DB=keycloak\n",
		"KC_HTTP_ENABLED=true\n",
		// No Cloudflare: Keycloak answers on whatever LAN address it's called.
		"KC_HOSTNAME_STRICT=false\n",
	} {
		if !strings.Contains(string(config), want) {
			t.Errorf("keycloak config.env lacks %q:\n%s", want, config)
		}
	}
	if strings.Contains(string(config), password) {
		t.Errorf("keycloak config.env: the password belongs in the secrets:\n%s", config)
	}
	sec, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/secrets.sops.env"))
	if !strings.Contains(string(sec), "KC_DB_PASSWORD="+password+"\n") || !strings.Contains(string(sec), "POSTGRES_PASSWORD="+password+"\n") {
		t.Errorf("the database password should be the stack's secret, for both containers:\n%s", sec)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/compose.yaml"))
	if !strings.Contains(string(compose), "command: start --optimized\n") || strings.Contains(string(compose), "start-dev") {
		t.Errorf("keycloak should run in production mode, on its pre-built image:\n%s", compose)
	}
	dockerfile, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/Dockerfile"))
	if !strings.Contains(string(dockerfile), "ENV KC_DB=postgres\n") || !strings.Contains(string(dockerfile), "kc.sh build") {
		t.Errorf("keycloak's image should be built for PostgreSQL:\n%s", dockerfile)
	}
	// Only Keycloak's port is published: the database stays on the
	// Stack's network.
	if strings.Count(string(compose), "ports:") != 1 || !strings.Contains(string(compose), `"8080:8080"`) {
		t.Errorf("only Keycloak's 8080 should be published:\n%s", compose)
	}

	// A re-run keeps the password.
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run again: %v", err)
	}
	if o.Credentials.KeycloakDBPassword != password {
		t.Error("a re-run generated a new database password")
	}
}

// With Cloudflare the public hostname is fixed: no lax hostname.
func TestRun_KeycloakHostnameWithCloudflare(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	withCloudflare(t, o)
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	config, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/config.env"))
	if !strings.Contains(string(config), "KC_HOSTNAME=https://auth-urbex.cubotto.net\n") || !strings.Contains(string(config), "KC_HTTP_ENABLED=true\n") || strings.Contains(string(config), "KC_HOSTNAME_STRICT") {
		t.Errorf("keycloak config.env with Cloudflare:\n%s", config)
	}
}
