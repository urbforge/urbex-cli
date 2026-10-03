package keycloak

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// Runs against a real Keycloak when URBEX_TEST_KEYCLOAK_URL and
// URBEX_TEST_KEYCLOAK_PASSWORD (the master admin's) are set; the realm it
// creates is deleted at the end.
func TestAgainstARealKeycloak(t *testing.T) {
	url, pass := os.Getenv("URBEX_TEST_KEYCLOAK_URL"), os.Getenv("URBEX_TEST_KEYCLOAK_PASSWORD")
	if url == "" {
		t.Skip("URBEX_TEST_KEYCLOAK_URL not set")
	}
	ctx := context.Background()
	c := New(url, "admin", pass)
	const realm = "urbex-test-probe"
	defer c.DeleteRealm(ctx, realm)

	if err := c.EnsureRealm(ctx, realm, "probe"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRealm(ctx, realm, "probe"); err != nil {
		t.Fatalf("a second EnsureRealm must be a no-op: %v", err)
	}
	if err := c.EnsureRoles(ctx, realm, []string{"admin", "user"}); err != nil {
		t.Fatal(err)
	}
	pc := PublicClient{ClientID: "web", Name: "probe web", RedirectURIs: []string{"https://staging.example.com/*"}, WebOrigins: []string{"https://staging.example.com"}}
	if err := c.EnsurePublicClient(ctx, realm, pc); err != nil {
		t.Fatal(err)
	}
	pc.RedirectURIs, pc.WebOrigins = []string{"https://prod.example.com/*"}, []string{"https://prod.example.com"}
	if err := c.EnsurePublicClient(ctx, realm, pc); err != nil {
		t.Fatal(err)
	}
	got, err := c.findClient(ctx, realm, "web")
	if err != nil || got == nil {
		t.Fatalf("client: %+v %v", got, err)
	}
	if strings.Join(got.RedirectURIs, ",") != "https://prod.example.com/*,https://staging.example.com/*" || !got.PublicClient || got.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Errorf("client = %+v", got)
	}
	if err := c.RemoveFromClient(ctx, realm, "web", []string{"https://staging.example.com/*"}, []string{"https://staging.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, _ = c.findClient(ctx, realm, "web")
	if strings.Join(got.RedirectURIs, ",") != "https://prod.example.com/*" || strings.Join(got.WebOrigins, ",") != "https://prod.example.com" {
		t.Errorf("after removing staging: %+v", got)
	}
	u := TestUser{Username: "test-admin", Password: "Pr0be-pass-1", Email: "test-admin@probe.test", FirstName: "Test", LastName: "admin", Roles: []string{"admin"}}
	if err := c.EnsureUser(ctx, realm, u); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureUser(ctx, realm, u); err != nil {
		t.Fatalf("a second EnsureUser must work: %v", err)
	}
	if err := c.EnsurePublicClient(ctx, realm, PublicClient{ClientID: "urbex-test", DirectAccessGrants: true}); err != nil {
		t.Fatal(err)
	}
	token, err := c.Token(ctx, realm, "urbex-test", "test-admin", "Pr0be-pass-1")
	if err != nil || token == "" {
		t.Fatalf("the test user should log in: %v", err)
	}
	if !strings.Contains(decodePayload(t, token), `"admin"`) {
		t.Errorf("the token should carry the admin role: %s", decodePayload(t, token))
	}
	if err := c.DeleteRealm(ctx, realm); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.RealmExists(ctx, realm); ok {
		t.Error("the realm should be gone")
	}
}

func decodePayload(t *testing.T, token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
