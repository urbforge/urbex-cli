package provision

import (
	"bytes"
	"context"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/technitium"
	"github.com/urbforge/urbex-cli/internal/technitium/technitiumtest"
)

// technitiumOrchestrator builds an Orchestrator wired to a fake
// Technitium, with its zone already created (bootstrap's job - ADR-0026 -
// not registerDNS's). Cloudflare's Name/Hostnames are set directly
// (platform.LoadFile would default them even without accountId/zoneId,
// but this test builds the config by hand); AccountID/ZoneID stay empty
// on purpose, so these tests prove DNS registration doesn't need
// Cloudflare enabled.
func technitiumOrchestrator(t *testing.T) (*Orchestrator, *technitiumtest.Server) {
	t.Helper()
	fake := technitiumtest.NewServer()
	t.Cleanup(fake.Close)
	cfg := validPlatformConfig()
	cfg.Domain = "cubotto.net"
	cfg.Cloudflare = platform.CloudflareConfig{Name: "urbex", Hostnames: platform.HostnamesFlat}
	dns := technitium.New(fake.URL, "tok")
	if err := dns.EnsureZone(context.Background(), cfg.Domain); err != nil {
		t.Fatal(err)
	}
	return &Orchestrator{
		Manifest: validManifest(), Config: cfg, Credentials: validCredentials(),
		Runner: &fakeRunner{}, Out: &bytes.Buffer{}, Rollout: &fakeRollout{},
		Technitium: dns,
	}, fake
}

func TestApply_RegistersInternalDNSRegardlessOfPublic(t *testing.T) {
	o, fake := technitiumOrchestrator(t)
	if o.Manifest.Services[0].Public {
		t.Fatal("test precondition: the service must not be public")
	}
	dir := t.TempDir()
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const host = "api-staging-my-app-urbex.cubotto.net"
	if got := fake.RecordIP("cubotto.net", host); got != "192.168.1.210" {
		t.Errorf("internal record for %s = %q, want the allocated IP", host, got)
	}
}

func TestDestroy_UnregistersInternalDNS(t *testing.T) {
	o, fake := technitiumOrchestrator(t)
	dir := t.TempDir()
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	const host = "api-staging-my-app-urbex.cubotto.net"
	if got := fake.RecordIP("cubotto.net", host); got != "" {
		t.Errorf("record should be gone, got IP %q", got)
	}
}

func TestInternalHostname_MatchesPublicHostnameForSplitHorizon(t *testing.T) {
	o, _ := technitiumOrchestrator(t)
	svc := o.Manifest.Services[0]
	// Now enable Cloudflare too (ADR-0026 option A: same domain, same
	// name, split-horizon).
	o.Config.Cloudflare.AccountID, o.Config.Cloudflare.ZoneID = "acc", "zone"
	if got, want := o.InternalHostname("staging", svc), o.PublicHostname("staging", svc); got != want {
		t.Errorf("InternalHostname = %q, PublicHostname = %q: split-horizon needs them equal", got, want)
	}
}

func TestApply_NoTechnitiumIsFine(t *testing.T) {
	o, _ := technitiumOrchestrator(t)
	o.Technitium = nil // not wired up yet (no token in credentials)
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Apply without Technitium configured should still succeed: %v", err)
	}
}
