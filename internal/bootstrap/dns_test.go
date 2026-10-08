package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/technitium"
	"github.com/urbforge/urbex-cli/internal/technitium/technitiumtest"
)

// A platform set up before ADR-0027 has its records in a zone named after
// the whole domain: bootstrap recreates the project services' records in
// the platform's subdomain, then deletes the old zone.
func TestSetupDNS_MigratesProjectRecordsFromTheDomainZone(t *testing.T) {
	fake := technitiumtest.NewServer()
	t.Cleanup(fake.Close)
	ctx := context.Background()
	old := technitium.New(fake.URL, "tok")
	if err := old.EnsureZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := old.SetRecord(ctx, "example.com", "api-staging-hello-urbex.example.com", "192.168.1.210"); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	for _, dir := range []string{"environments/staging/hello/api", "environments/prod/hello/api", "environments/staging/hello/web"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ledger := &allocation.Ledger{Entries: map[string]allocation.Entry{
		"hello-api-staging": {VMID: 9100, IP: "192.168.1.210"},
		"hello-api-prod":    {VMID: 9101, IP: "192.168.1.211"},
	}}
	if err := ledger.Save(filepath.Join(repo, "state", "allocations.json")); err != nil {
		t.Fatal(err)
	}

	creds := validCredentials()
	creds.TechnitiumToken = "tok"
	o := &Orchestrator{Config: validConfig(), Credentials: creds, Out: &bytes.Buffer{}}
	clients := Clients{Technitium: func(u, tok string) TechnitiumAPI { return technitium.New(u, tok) }}
	ips, _, err := allocateIPs(o.Config)
	if err != nil {
		t.Fatal(err)
	}
	ep := Endpoints{TechnitiumURL: fake.URL}
	if err := o.setupDNS(ctx, clients, ep, ips, repo); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"api.staging.hello.urbex.example.com": "192.168.1.210",
		"api.hello.urbex.example.com":         "192.168.1.211",
		"git.urbex.example.com":               ips["urbex-gitea"],
	} {
		if got := fake.RecordIP("urbex.example.com", name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if fake.HasZone("example.com") {
		t.Error("the old domain-named zone should be gone")
	}
	if got := fake.Forwarders; len(got) != 1 || got[0] != "192.168.1.1" {
		t.Errorf("forwarders = %v, want the gateway", got)
	}

	// Re-running bootstrap finds the old zone gone: nothing to fail on.
	if err := o.setupDNS(ctx, clients, ep, ips, repo); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// Without Cloudflare there is no tunnel LXC, and no tunnel record.
func TestPlatformRecords(t *testing.T) {
	cfg := validConfig()
	ips, _, err := allocateIPs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := PlatformRecords(cfg, ips)
	if _, ok := records["tunnel.urbex.example.com"]; ok {
		t.Error("no tunnel record without Cloudflare")
	}
	if records["grafana.urbex.example.com"] != ips["urbex-observability"] || records["prometheus.urbex.example.com"] != ips["urbex-observability"] {
		t.Errorf("grafana and prometheus share the observability LXC: %v", records)
	}
	if len(records) != 8 {
		t.Errorf("got %d records, want 8: %v", len(records), records)
	}
}
