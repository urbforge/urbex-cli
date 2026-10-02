package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/cloudflare/cloudflaretest"
	"github.com/urbforge/urbex-cli/internal/platform"
)

// withCloudflare turns Cloudflare on in o's config and points it at a fake
// Cloudflare API.
func withCloudflare(t *testing.T, o *Orchestrator) *cloudflaretest.Server {
	t.Helper()
	fake := cloudflaretest.NewServer()
	t.Cleanup(fake.Close)
	o.Config.Domain = "cubotto.net"
	o.Config.Cloudflare = platform.CloudflareConfig{
		AccountID: "acc", ZoneID: "zone", Name: "urbex", Hostnames: platform.HostnamesFlat,
		TunnelName: "urbex-platform", Access: platform.AccessConfig{Emails: []string{"me@example.com"}},
	}
	o.Credentials.CloudflareToken = "cf-token"
	o.Clients.Cloudflare = func(token, account, zone string) CloudflareAPI {
		return cloudflare.New(fake.URL, token, account, zone)
	}
	return fake
}

func TestRun_PublishesThePlatformOnCloudflare(t *testing.T) {
	dir := t.TempDir()
	o, runner, fp := newRunOrchestrator(t, &fakeProxmox{})
	fake := withCloudflare(t, o)
	fake.IdPs = nil // a new Zero Trust organization: no login method yet

	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fake.Tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(fake.Tunnels))
	}
	var tunnelID string
	for id, tun := range fake.Tunnels {
		tunnelID = id
		if tun.Name != "urbex-platform" {
			t.Errorf("tunnel name %q", tun.Name)
		}
	}
	routes := fake.RoutesOf(tunnelID)
	want := map[string]string{
		"auth-urbex.cubotto.net":   "http://192.168.1.203:8080",
		"git-urbex.cubotto.net":    "http://192.168.1.200:3000",
		"komodo-urbex.cubotto.net": "http://192.168.1.201:9120",
		"hooks-urbex.cubotto.net":  "http://192.168.1.201:9120 ^/listener/",
	}
	for h, svc := range want {
		if routes[h] != svc {
			t.Errorf("route %s = %q, want %q", h, routes[h], svc)
		}
	}
	names := fake.RecordNames()
	sort.Strings(names)
	if strings.Join(names, ",") != "auth-urbex.cubotto.net,git-urbex.cubotto.net,hooks-urbex.cubotto.net,komodo-urbex.cubotto.net" {
		t.Errorf("DNS records = %v", names)
	}
	var protected []string
	for _, app := range fake.Apps {
		protected = append(protected, app["domain"].(string))
	}
	sort.Strings(protected)
	if strings.Join(protected, ",") != "git-urbex.cubotto.net,komodo-urbex.cubotto.net" {
		t.Errorf("Access protects %v, want Gitea and Komodo only", protected)
	}
	if fp.variables["URBEX_CLOUDFLARE_TOKEN"] != "secret:cf-token" || fp.variables["URBEX_CLOUDFLARE_ACCOUNT_ID"] != "acc" {
		t.Errorf("Komodo needs the Cloudflare token and account for the web frontends, variables = %v", fp.variables)
	}
	if len(fake.IdPs) != 1 || fake.IdPs[0] != "onetimepin" {
		t.Errorf("expected the one-time PIN login to be enabled, got %v", fake.IdPs)
	}

	// The connector's token reaches Ansible through the environment only.
	ansible := runner.calls[len(runner.calls)-1]
	found := false
	for _, e := range ansible.Env {
		found = found || e == "URBEX_CLOUDFLARE_TUNNEL_TOKEN=token-of-"+tunnelID
	}
	if !found {
		t.Error("ansible-playbook should get the tunnel's token in its environment")
	}
	inventory, _ := os.ReadFile(filepath.Join(dir, "ansible", "inventory.yml"))
	for _, want := range []string{"urbex_tunnel", "192.168.1.205", "keycloak_public_url: https://auth-urbex.cubotto.net"} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("inventory lacks %q:\n%s", want, inventory)
		}
	}
	tfvars, _ := os.ReadFile(filepath.Join(dir, "terraform", "base", "terraform.auto.tfvars.json"))
	if !strings.Contains(string(tfvars), `"urbex-tunnel"`) {
		t.Errorf("terraform should create the tunnel LXC:\n%s", tfvars)
	}
	for _, f := range []string{"inventory.yml"} {
		data, _ := os.ReadFile(filepath.Join(dir, "ansible", f))
		if strings.Contains(string(data), "token-of-") {
			t.Errorf("the tunnel token must not be written to %s", f)
		}
	}
}

func TestRun_WithoutCloudflareNoTunnel(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	inventory, _ := os.ReadFile(filepath.Join(dir, "ansible", "inventory.yml"))
	tfvars, _ := os.ReadFile(filepath.Join(dir, "terraform", "base", "terraform.auto.tfvars.json"))
	if strings.Contains(string(inventory), "urbex_tunnel") || strings.Contains(string(tfvars), "urbex-tunnel") || strings.Contains(string(inventory), "keycloak_public_url") {
		t.Error("a platform without Cloudflare must have no tunnel LXC and no public Keycloak URL")
	}
}

func TestRun_CloudflareNeedsItsToken(t *testing.T) {
	o, runner, _ := newRunOrchestrator(t, &fakeProxmox{})
	withCloudflare(t, o)
	o.Credentials.CloudflareToken = ""
	err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "URBEX_CLOUDFLARE_TOKEN") || len(runner.calls) != 0 {
		t.Errorf("expected a refusal naming the token before anything runs, got %v", err)
	}
}

func TestTeardown_RemovesThePlatformFromCloudflare(t *testing.T) {
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	fake := withCloudflare(t, o)
	dir := gitopsRepo(t)
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Something else in the zone, not urbex's.
	fake.Records["theirs"] = &cloudflaretest.Record{ID: "theirs", Type: "A", Name: "www.cubotto.net"}

	if err := o.Teardown(context.Background(), TeardownOptions{GitOpsRepoPath: dir, Yes: true}); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	for _, tun := range fake.Tunnels {
		if !tun.Deleted {
			t.Errorf("tunnel %s should be deleted", tun.Name)
		}
	}
	if names := fake.RecordNames(); len(names) != 1 || names[0] != "www.cubotto.net" {
		t.Errorf("only urbex's records should be gone, left %v", names)
	}
	if len(fake.Apps) != 0 || len(fake.Policies) != 0 {
		t.Errorf("Access objects left: %d apps, %d policies", len(fake.Apps), len(fake.Policies))
	}
}

// Found on a real account: routes published before Access failed left
// Gitea and Komodo open to the internet. Access comes first, and if it
// can't be set up, nothing is published.
func TestRun_NothingPublishedWhenAccessCantBeSetUp(t *testing.T) {
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	fake := withCloudflare(t, o)
	fake.Forbidden["access/identity_providers"] = false
	fake.IdPs = nil
	// The token may list login methods but not add one.
	o.Clients.Cloudflare = func(token, account, zone string) CloudflareAPI {
		return noPINAllowed{cloudflare.New(fake.URL, token, account, zone)}
	}

	err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "One-time PIN") {
		t.Fatalf("expected a failure about the login method, got %v", err)
	}
	for id := range fake.Tunnels {
		if routes := fake.RoutesOf(id); len(routes) != 0 {
			t.Errorf("nothing may be routed without Access, got %v", routes)
		}
	}
	if names := fake.RecordNames(); len(names) != 0 {
		t.Errorf("no DNS record may exist without Access, got %v", names)
	}
}

type noPINAllowed struct{ *cloudflare.Client }

func (noPINAllowed) EnableOneTimePIN(context.Context) error {
	return &cloudflare.APIError{Status: 403}
}

// Every base service's LXC runs the telemetry agent too, labelled as the
// platform's, with its service name.
func TestRun_BaseInventoryLabelsPlatformTelemetry(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	inventory, _ := os.ReadFile(filepath.Join(dir, "ansible", "inventory.yml"))
	for _, want := range []string{
		"urbex_kind: platform", "telemetry_compose_project: urbex",
		"loki_push_url: http://192.168.1.204:3100/loki/api/v1/push",
		"prometheus_write_url: http://192.168.1.204:9090/api/v1/write",
		"urbex_service: gitea", "urbex_service: observability",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("base inventory lacks %q:\n%s", want, inventory)
		}
	}
	playbook, _ := os.ReadFile(filepath.Join(dir, "ansible", "playbook.yml"))
	if !strings.Contains(string(playbook), "- telemetry") {
		t.Error("the base playbook should install the telemetry agent")
	}
}
