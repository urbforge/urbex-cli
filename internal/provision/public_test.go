package provision

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/cloudflare/cloudflaretest"
	"github.com/urbforge/urbex-cli/internal/platform"
)

func cloudflareOrchestrator(t *testing.T) (*Orchestrator, *cloudflaretest.Server, string) {
	t.Helper()
	fake := cloudflaretest.NewServer()
	t.Cleanup(fake.Close)
	cfg := validPlatformConfig()
	cfg.Domain = "cubotto.net"
	cfg.Cloudflare = platform.CloudflareConfig{AccountID: "acc", ZoneID: "zone", Name: "urbex", Hostnames: platform.HostnamesFlat, TunnelName: "urbex-platform", Access: platform.AccessConfig{Emails: []string{"me@example.com"}}}
	creds := validCredentials()
	creds.CloudflareToken = "cf"
	m := validManifest()
	m.Services[0].Public = true
	cf := cloudflare.New(fake.URL, "cf", "acc", "zone")
	tunnel, err := cf.EnsureTunnel(context.Background(), "urbex-platform")
	if err != nil {
		t.Fatal(err)
	}
	return &Orchestrator{
		Manifest: m, Config: cfg, Credentials: creds,
		Runner: &fakeRunner{}, Out: &bytes.Buffer{}, Rollout: &fakeRollout{},
		Cloudflare: cf,
	}, fake, tunnel.ID
}

func TestApply_PublishesPublicServicesOnly(t *testing.T) {
	o, fake, tunnel := cloudflareOrchestrator(t)
	dir := t.TempDir()
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	routes := fake.RoutesOf(tunnel)
	want := map[string]string{
		"api-staging-my-app-urbex.cubotto.net": "http://192.168.1.210:8080",
		"api-my-app-urbex.cubotto.net":         "http://192.168.1.211:8080",
	}
	if len(routes) != len(want) {
		t.Errorf("routes = %v, want %v", routes, want)
	}
	for h, svc := range want {
		if routes[h] != svc {
			t.Errorf("route %s = %q, want %q", h, routes[h], svc)
		}
	}
	names := fake.RecordNames()
	sort.Strings(names)
	if strings.Join(names, ",") != "api-my-app-urbex.cubotto.net,api-staging-my-app-urbex.cubotto.net" {
		t.Errorf("DNS records = %v", names)
	}

	// No longer public: its route and record go on the next apply.
	o.Manifest.Services[0].Public = false
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if routes := fake.RoutesOf(tunnel); len(routes) != 1 || routes["api-my-app-urbex.cubotto.net"] == "" {
		t.Errorf("only prod should stay published, routes = %v", routes)
	}
	if names := fake.RecordNames(); len(names) != 1 {
		t.Errorf("staging's record should be gone, left %v", names)
	}
}

func TestDestroy_Unpublishes(t *testing.T) {
	o, fake, tunnel := cloudflareOrchestrator(t)
	dir := t.TempDir()
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if routes := fake.RoutesOf(tunnel); len(routes) != 0 {
		t.Errorf("routes left: %v", routes)
	}
	if names := fake.RecordNames(); len(names) != 0 {
		t.Errorf("DNS records left: %v", names)
	}
}

func TestApply_CloudflareWithoutTokenIsRefused(t *testing.T) {
	o, _, _ := cloudflareOrchestrator(t)
	o.Credentials.CloudflareToken = ""
	err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "URBEX_CLOUDFLARE_TOKEN") {
		t.Errorf("expected a refusal naming the token, got %v", err)
	}
}
