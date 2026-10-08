package provision

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/technitium"
	"github.com/urbforge/urbex-cli/internal/technitium/technitiumtest"
)

// technitiumOrchestrator builds an Orchestrator wired to a fake
// Technitium, with its zone already created (bootstrap's job - ADR-0027 -
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
	if err := dns.EnsureZone(context.Background(), cfg.InternalZone()); err != nil {
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
	const host = "api.staging.my-app.urbex.cubotto.net"
	if got := fake.RecordIP("urbex.cubotto.net", host); got != "192.168.1.210" {
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
	const host = "api.staging.my-app.urbex.cubotto.net"
	if got := fake.RecordIP("urbex.cubotto.net", host); got != "" {
		t.Errorf("record should be gone, got IP %q", got)
	}
}

// Internal names are the nested form of the public hostnames, whatever
// cloudflare.hostnames says: with nested public names, a service has one
// name inside and out (ADR-0027).
func TestInternalHostname_IsTheNestedPublicHostname(t *testing.T) {
	o, _ := technitiumOrchestrator(t)
	svc := o.Manifest.Services[0]
	if got, want := o.InternalHostname("staging", svc), "api.staging.my-app.urbex.cubotto.net"; got != want {
		t.Errorf("InternalHostname(staging) = %q, want %q", got, want)
	}
	if got, want := o.InternalHostname("prod", svc), "api.my-app.urbex.cubotto.net"; got != want {
		t.Errorf("InternalHostname(prod) = %q, want %q", got, want)
	}
	o.Config.Cloudflare.Hostnames = platform.HostnamesNested
	if got, want := o.InternalHostname("staging", svc), o.hostname("staging", svc); got != want {
		t.Errorf("with nested hostnames, InternalHostname = %q, public hostname = %q: they should be equal", got, want)
	}
}

func TestApply_RefusesAProjectNamedLikeAPlatformService(t *testing.T) {
	o, _ := technitiumOrchestrator(t)
	o.Manifest.Project = "git"
	err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "git.urbex.cubotto.net") {
		t.Fatalf("Apply of a project named git = %v, want an error naming the clash", err)
	}
}

func TestApply_NoTechnitiumIsFine(t *testing.T) {
	o, _ := technitiumOrchestrator(t)
	o.Technitium = nil // not wired up yet (no token in credentials)
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Apply without Technitium configured should still succeed: %v", err)
	}
}
