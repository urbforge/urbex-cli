package cloudflare

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/cloudflare/cloudflaretest"
)

func newTestClient(t *testing.T) (*Client, *cloudflaretest.Server) {
	t.Helper()
	fake := cloudflaretest.NewServer()
	t.Cleanup(fake.Close)
	return New(fake.URL, "tok", "acc", "zone"), fake
}

func TestTunnel_EnsuredOnceAndRoutedByHostname(t *testing.T) {
	ctx := context.Background()
	c, fake := newTestClient(t)

	tunnel, err := c.EnsureTunnel(ctx, "urbex-platform")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.EnsureTunnel(ctx, "urbex-platform")
	if err != nil || again.ID != tunnel.ID {
		t.Fatalf("a second EnsureTunnel must find the first one: %+v, %v", again, err)
	}

	if err := c.SetRoutes(ctx, tunnel.ID, []string{"auth-urbex.d", "git-urbex.d"}, []Route{
		{Hostname: "auth-urbex.d", Service: "http://10.0.0.3:8080"},
		{Hostname: "git-urbex.d", Service: "http://10.0.0.1:3000"},
	}); err != nil {
		t.Fatal(err)
	}
	// Another owner's routes (a project's) are kept when the platform's change.
	if err := c.SetRoutes(ctx, tunnel.ID, []string{"api-hello-urbex.d"}, []Route{{Hostname: "api-hello-urbex.d", Service: "http://10.0.0.10:8080"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutes(ctx, tunnel.ID, []string{"git-urbex.d"}, nil); err != nil {
		t.Fatal(err)
	}
	got := fake.RoutesOf(tunnel.ID)
	want := map[string]string{"auth-urbex.d": "http://10.0.0.3:8080", "api-hello-urbex.d": "http://10.0.0.10:8080"}
	if len(got) != len(want) || got["auth-urbex.d"] != want["auth-urbex.d"] || got["api-hello-urbex.d"] != want["api-hello-urbex.d"] {
		t.Errorf("routes = %v, want %v", got, want)
	}
	last := fake.Tunnels[tunnel.ID].Ingress[len(fake.Tunnels[tunnel.ID].Ingress)-1]
	if last["service"] != "http_status:404" || last["hostname"] != nil {
		t.Errorf("the ingress must end with the catch-all, ends with %v", last)
	}

	if token, err := c.TunnelToken(ctx, tunnel.ID); err != nil || token == "" {
		t.Errorf("TunnelToken = %q, %v", token, err)
	}
	if err := c.DeleteTunnel(ctx, tunnel.ID); err != nil {
		t.Fatal(err)
	}
	if gone, _ := c.FindTunnel(ctx, "urbex-platform"); gone != nil {
		t.Error("the tunnel should be gone")
	}
}

func TestDNS_OnlyUrbexRecordsAreChanged(t *testing.T) {
	ctx := context.Background()
	c, fake := newTestClient(t)

	if err := c.EnsureTunnelRecord(ctx, "auth-urbex.d", "tun1"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureTunnelRecord(ctx, "auth-urbex.d", "tun2"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Records) != 1 {
		t.Fatalf("expected one record, got %v", fake.RecordNames())
	}
	for _, r := range fake.Records {
		if r.Content != "tun2.cfargotunnel.com" || !r.Proxied || r.Type != "CNAME" {
			t.Errorf("unexpected record %+v", r)
		}
	}

	fake.Records["mine"] = &cloudflaretest.Record{ID: "mine", Type: "A", Name: "www.d", Content: "1.2.3.4"}
	if err := c.EnsureTunnelRecord(ctx, "www.d", "tun2"); err == nil || !strings.Contains(err.Error(), "wasn't created by urbex") {
		t.Errorf("a record urbex didn't create must not be overwritten, got %v", err)
	}
	if err := c.DeleteRecord(ctx, "www.d"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRecord(ctx, "auth-urbex.d"); err != nil {
		t.Fatal(err)
	}
	if names := fake.RecordNames(); len(names) != 1 || names[0] != "www.d" {
		t.Errorf("only urbex's record should be deleted, left %v", names)
	}
}

func TestAccess_PolicyAppsAndLogin(t *testing.T) {
	ctx := context.Background()
	c, fake := newTestClient(t)

	id, err := c.EnsureEmailPolicy(ctx, "urbex-operators", []string{"a@x", "b@y"})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.EnsureEmailPolicy(ctx, "urbex-operators", []string{"a@x"}); again != id || len(fake.Policies) != 1 {
		t.Errorf("the policy must be updated in place, got %s and %d policies", again, len(fake.Policies))
	}
	for _, name := range []string{"urbex-git", "urbex-komodo"} {
		if err := c.EnsureAccessApp(ctx, name, name+".d", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.EnsureAccessApp(ctx, "urbex-git", "git.d", id); err != nil {
		t.Fatal(err)
	}
	var domains []string
	for _, a := range fake.Apps {
		domains = append(domains, a["domain"].(string))
	}
	sort.Strings(domains)
	if strings.Join(domains, ",") != "git.d,urbex-komodo.d" {
		t.Errorf("apps = %v", domains)
	}
	if err := c.DeleteAccessApp(ctx, "urbex-git"); err != nil || len(fake.Apps) != 1 {
		t.Errorf("DeleteAccessApp: %v, %d apps left", err, len(fake.Apps))
	}
	if err := c.DeletePolicy(ctx, "urbex-operators"); err != nil || len(fake.Policies) != 0 {
		t.Errorf("DeletePolicy: %v, %d left", err, len(fake.Policies))
	}

	if ok, _ := c.HasOneTimePIN(ctx); ok {
		t.Error("no login method yet")
	}
	if err := c.EnableOneTimePIN(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.HasOneTimePIN(ctx); !ok {
		t.Error("one-time PIN should now be enabled")
	}
}

func TestForbidden(t *testing.T) {
	c, fake := newTestClient(t)
	fake.Forbidden["access/identity_providers"] = true
	_, err := c.HasOneTimePIN(context.Background())
	if !Forbidden(err) {
		t.Errorf("expected a permission error, got %v", err)
	}
}

// Access answers a missing permission with {"code":1010,"error":"auth.forbidden"}.
func TestAPIError_AccessStyle(t *testing.T) {
	err := &APIError{Status: 403, Errors: []apiErrorItem{{Code: 1010, Err: "auth.forbidden"}}}
	if !strings.Contains(err.Error(), "auth.forbidden (1010)") || !Forbidden(err) {
		t.Errorf("got %q, forbidden=%v", err.Error(), Forbidden(err))
	}
}
