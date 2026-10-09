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
		"auth-urbex.cubotto.net":       "http://auth.urbex.cubotto.net:8080",
		"git-urbex.cubotto.net":        "http://git.urbex.cubotto.net:3000",
		"komodo-urbex.cubotto.net":     "http://komodo.urbex.cubotto.net:9120",
		"hooks-urbex.cubotto.net":      "http://komodo.urbex.cubotto.net:9120 ^/listener/",
		"dns-urbex.cubotto.net":        "http://dns.urbex.cubotto.net:5380",
		"grafana-urbex.cubotto.net":    "http://grafana.urbex.cubotto.net:3000",
		"prometheus-urbex.cubotto.net": "http://prometheus.urbex.cubotto.net:9090",
		"loki-urbex.cubotto.net":       "http://loki.urbex.cubotto.net:3100",
	}
	for h, svc := range want {
		if routes[h] != svc {
			t.Errorf("route %s = %q, want %q", h, routes[h], svc)
		}
	}
	names := fake.RecordNames()
	sort.Strings(names)
	if strings.Join(names, ",") != "auth-urbex.cubotto.net,dns-urbex.cubotto.net,git-urbex.cubotto.net,grafana-urbex.cubotto.net,hooks-urbex.cubotto.net,komodo-urbex.cubotto.net,loki-urbex.cubotto.net,prometheus-urbex.cubotto.net" {
		t.Errorf("DNS records = %v", names)
	}
	var protected []string
	for _, app := range fake.Apps {
		protected = append(protected, app["domain"].(string))
	}
	sort.Strings(protected)
	// Admin and internal UIs behind Access (ADR-0027); Keycloak's realms
	// and the webhooks stay public.
	wantProtected := "auth-urbex.cubotto.net/admin,auth-urbex.cubotto.net/realms/master,dns-urbex.cubotto.net,git-urbex.cubotto.net,grafana-urbex.cubotto.net,komodo-urbex.cubotto.net,loki-urbex.cubotto.net,prometheus-urbex.cubotto.net"
	if strings.Join(protected, ",") != wantProtected {
		t.Errorf("Access protects %v, want %s", protected, wantProtected)
	}
	if fp.variables["URBEX_CLOUDFLARE_TOKEN"] != "secret:cf-token" || fp.variables["URBEX_CLOUDFLARE_ACCOUNT_ID"] != "acc" {
		t.Errorf("Komodo needs the Cloudflare token and account for the web frontends, variables = %v", fp.variables)
	}
	if len(fake.IdPs) != 1 || fake.IdPs[0] != "onetimepin" {
		t.Errorf("expected the one-time PIN login to be enabled, got %v", fake.IdPs)
	}

	// The connector's token reaches Ansible through the environment only.
	ansible := runner.calls[2] // playbook.yml: the base services
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
		"urbex_kind: platform",
		"loki_push_url: http://loki.urbex.example.com:3100/loki/api/v1/push",
		"prometheus_write_url: http://prometheus.urbex.example.com:9090/api/v1/write",
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

// The base services' LXCs become Komodo Servers (Periphery, but on
// Komodo's own LXC which has it), and the platform's Komodo resources are
// tagged "platform", as on Grafana.
func TestRun_ConnectsAndTagsThePlatformInKomodo(t *testing.T) {
	dir := t.TempDir()
	o, runner, fp := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	last := runner.calls[len(runner.calls)-1]
	if last.Name != "ansible-playbook" || last.Args[len(last.Args)-1] != "platform-periphery.yml" {
		t.Errorf("expected the platform-periphery playbook last, got %+v", last)
	}
	if strings.Join(fp.waitedServers, ",") != "urbex-gitea,urbex-technitium,urbex-keycloak,urbex-observability" {
		t.Errorf("waited for %v", fp.waitedServers)
	}
	for _, r := range []string{"Server urbex-gitea", "Server urbex-komodo", "Server urbex-observability", "Builder urbex-komodo", "Action urbex-release", "Procedure urbex-gitops"} {
		if strings.Join(fp.tags[r], ",") != "platform" {
			t.Errorf("%s tags = %v, want platform", r, fp.tags[r])
		}
	}
	inventory, _ := os.ReadFile(filepath.Join(dir, "ansible", "inventory.yml"))
	if !strings.Contains(string(inventory), "komodo_core_address: http://komodo.urbex.example.com:9120") {
		t.Errorf("the inventory needs Komodo's address for Periphery:\n%s", inventory)
	}
}

// Prometheus' remote write and Loki's push stay internal (ADR-0027): on
// their public hostnames those paths answer 404, and the rule comes
// before the hostname's own, or cloudflared would never reach it.
func TestRun_WritePathsStayInternal(t *testing.T) {
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	fake := withCloudflare(t, o)
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var ingress []string
	for id := range fake.Tunnels {
		ingress = fake.IngressOf(id)
	}
	index := func(rule string) int {
		for i, r := range ingress {
			if r == rule {
				return i
			}
		}
		t.Fatalf("no rule %q in %v", rule, ingress)
		return -1
	}
	if index("prometheus-urbex.cubotto.net ^/api/v1/(write|otlp) -> http_status:404") > index("prometheus-urbex.cubotto.net -> http://prometheus.urbex.cubotto.net:9090") {
		t.Error("Prometheus' write block must come before its route")
	}
	if index("loki-urbex.cubotto.net ^/(loki/api/v1/push|api/prom/push|otlp/) -> http_status:404") > index("loki-urbex.cubotto.net -> http://loki.urbex.cubotto.net:3100") {
		t.Error("Loki's push block must come before its route")
	}
}
