package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
	o, _, fp := newRunOrchestrator(t, &fakeProxmox{})
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
		"hooks-urbex.cubotto.net":      "http://hooks.urbex.cubotto.net:9120 ^/listener/",
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

	// The connector's token is its Stack's secret now, not Ansible's.
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
		// The images Komodo deploys later, pulled by the parallel play.
		"- quay.io/keycloak/keycloak:", "- docker.io/grafana/grafana:", "- docker.io/prom/prometheus:",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("base inventory lacks %q:\n%s", want, inventory)
		}
	}
	playbook, _ := os.ReadFile(filepath.Join(dir, "ansible", "playbook.yml"))
	if !strings.Contains(string(playbook), "- telemetry") {
		t.Error("the base playbook should install the telemetry agent")
	}
	if !strings.Contains(string(playbook), "docker pull --quiet {{ item }}") {
		t.Error("the parallel play should pull each LXC's Stack images")
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

// Technitium, Gitea and Komodo become Komodo Stacks with their compose
// files on their servers (ADR-0027), adopting the running compose project
// "urbex" rather than redeploying it, tagged platform.
func TestRun_KomodoAdoptsTheBootstrapServices(t *testing.T) {
	o, _, fp := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"urbex-technitium", "urbex-gitea", "urbex-komodo"} {
		cfg, ok := fp.stacks[name]
		if !ok {
			t.Errorf("no stack %s, stacks: %v", name, fp.stacks)
			continue
		}
		if cfg.ServerID != name || !cfg.FilesOnHost || cfg.RunDirectory != "/opt/urbex" || cfg.ProjectName != "urbex" || cfg.Repo != "" {
			t.Errorf("stack %s = %+v, want files on host in /opt/urbex, project urbex, on server %s", name, cfg, name)
		}
		if strings.Join(fp.tags["Stack "+name], ",") != "platform" {
			t.Errorf("stack %s tags = %v", name, fp.tags["Stack "+name])
		}
	}
	if cfg := fp.stacks["urbex-keycloak"]; cfg.FilesOnHost {
		t.Error("Keycloak isn't a bootstrap service: it comes from the GitOps repo")
	}
}

// Keycloak, the observability stack and the tunnel are Komodo Stacks from
// the GitOps repo's platform/ folder (ADR-0027): files, configuration
// and encrypted secrets written by bootstrap, deployed by Komodo, the
// secrets never in the plain configuration nor in Ansible's environment.
func TestRun_PlatformStacksFromTheGitOpsRepo(t *testing.T) {
	dir := t.TempDir()
	o, runner, fp := newRunOrchestrator(t, &fakeProxmox{})
	withCloudflare(t, o)
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range []string{
		"platform/keycloak/compose.yaml", "platform/keycloak/config.env", "platform/keycloak/secrets.sops.env",
		"platform/observability/compose.yaml", "platform/observability/prometheus.yml",
		"platform/observability/grafana/dashboards/urbex-resources.json",
		"platform/observability/grafana/provisioning/datasources/urbex.yml",
		"platform/tunnel/compose.yaml", "platform/tunnel/secrets.sops.env",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s in the gitops repo", f)
		}
	}
	config, _ := os.ReadFile(filepath.Join(dir, "platform/keycloak/config.env"))
	if !strings.Contains(string(config), "KC_HOSTNAME=https://auth-urbex.cubotto.net") || o.Credentials.KeycloakAdminPassword == "" || strings.Contains(string(config), o.Credentials.KeycloakAdminPassword) {
		t.Errorf("keycloak config.env: public hostname expected, no password:\n%s", config)
	}
	sec, _ := os.ReadFile(filepath.Join(dir, "platform/tunnel/secrets.sops.env"))
	if !strings.Contains(string(sec), "TUNNEL_TOKEN=token-of-") {
		t.Errorf("the tunnel's token should be its stack's secret:\n%s", sec)
	}
	for _, name := range []string{"urbex-keycloak", "urbex-observability", "urbex-tunnel"} {
		cfg := fp.stacks[name]
		if cfg.ServerID != name || cfg.RunDirectory != "platform/"+name[len("urbex-"):] || cfg.Repo != "urbex/gitops" || cfg.ProjectName != "urbex" || cfg.ComposeCmdWrapper != ComposeWrapper {
			t.Errorf("stack %s = %+v", name, cfg)
		}
		if strings.Join(fp.tags["Stack "+name], ",") != "platform" {
			t.Errorf("stack %s tags = %v", name, fp.tags["Stack "+name])
		}
	}
	want := "urbex-keycloak,urbex-observability,urbex-tunnel," +
		"urbex-telemetry-urbex-gitea,urbex-telemetry-urbex-komodo,urbex-telemetry-urbex-technitium," +
		"urbex-telemetry-urbex-keycloak,urbex-telemetry-urbex-observability,urbex-telemetry-urbex-tunnel"
	// All at once (urbforge/urbex-cli#30): in any order.
	sort.Strings(fp.deployed)
	wantSorted := strings.Split(want, ",")
	sort.Strings(wantSorted)
	if strings.Join(fp.deployed, ",") != strings.Join(wantSorted, ",") {
		t.Errorf("deployed %v", fp.deployed)
	}
	// Every LXC's telemetry agent: a Stack of its own, kind agent, its
	// config labelled with the LXC's main service.
	cfg := fp.stacks["urbex-telemetry-urbex-gitea"]
	if cfg.ServerID != "urbex-gitea" || cfg.RunDirectory != "platform/telemetry/urbex-gitea" || cfg.ProjectName != "telemetry" {
		t.Errorf("telemetry stack = %+v", cfg)
	}
	if strings.Join(fp.tags["Stack urbex-telemetry-urbex-gitea"], ",") != "agent" {
		t.Errorf("telemetry stack tags = %v", fp.tags["Stack urbex-telemetry-urbex-gitea"])
	}
	alloy, _ := os.ReadFile(filepath.Join(dir, "platform/telemetry/urbex-gitea/config.alloy"))
	for _, want := range []string{`replacement  = "platform"`, `replacement  = "gitea"`, "http://loki.urbex.cubotto.net:3100/loki/api/v1/push", `host = "urbex-gitea"`} {
		if !strings.Contains(string(alloy), want) {
			t.Errorf("config.alloy lacks %q:\n%s", want, alloy)
		}
	}
	for _, call := range runner.calls {
		for _, e := range call.Env {
			if strings.HasPrefix(e, "URBEX_KEYCLOAK_ADMIN_PASSWORD=") || strings.HasPrefix(e, "URBEX_GRAFANA_ADMIN_PASSWORD=") || strings.HasPrefix(e, "URBEX_CLOUDFLARE_TUNNEL_TOKEN=") {
				t.Errorf("%s %v gets %s: these are the Stacks' secrets now", call.Name, call.Args, strings.SplitN(e, "=", 2)[0])
			}
		}
	}
}

// The Stacks are deployed all at once: a failed one doesn't stop the
// others, and fails the bootstrap with its name (urbforge/urbex-cli#30).
func TestRun_AFailedStackDeployNamesIt(t *testing.T) {
	o, _, fp := newRunOrchestrator(t, &fakeProxmox{})
	fp.deployFails = map[string]bool{"urbex-observability": true}
	err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "urbex-observability") || strings.Contains(err.Error(), "urbex-keycloak") {
		t.Fatalf("Run = %v, want an error naming urbex-observability only", err)
	}
	if !slices.Contains(fp.deployed, "urbex-keycloak") || !slices.Contains(fp.deployed, "urbex-telemetry-urbex-gitea") {
		t.Errorf("the other Stacks should still be deployed: %v", fp.deployed)
	}
}

// Every base service's LXC runs services of the catalog (ADR-0027): a new
// base service needs its catalog entries.
func TestBaseServicesAreInTheCatalog(t *testing.T) {
	cfg := validConfig()
	cfg.Cloudflare = platform.CloudflareConfig{AccountID: "a", ZoneID: "z", Name: "urbex"}
	for _, svc := range BaseServicesFor(cfg) {
		found := false
		for _, s := range platform.Catalog {
			found = found || s.LXC == svc.Name
		}
		if !found {
			t.Errorf("base service %s has no entry in the service catalog (internal/platform/catalog.go)", svc.Name)
		}
	}
}
