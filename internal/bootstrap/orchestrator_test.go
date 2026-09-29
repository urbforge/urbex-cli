package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
)

var errProxmoxUnreachable = errors.New("proxmox unreachable")

type fakeProxmox struct {
	hostnames []string
	err       error
}

func (f *fakeProxmox) ListContainerHostnames(ctx context.Context, node string) ([]string, error) {
	return f.hostnames, f.err
}

type fakeRunner struct {
	calls []Command
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, c Command) error {
	f.calls = append(f.calls, c)
	return f.err
}

func validConfig() *platform.Config {
	return &platform.Config{
		Domain: "example.com",
		Git:    platform.GitConfig{GiteaURL: "https://git.example.com", Org: "urbex"},
		Proxmox: platform.ProxmoxConfig{
			APIURL:       "https://proxmox.example.internal:8006/api2/json",
			Node:         "pve",
			StoragePool:  "local-lvm",
			LXCTemplate:  "local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst",
			SSHPublicKey: "ssh-ed25519 AAAA... test",
			VMIDBase:     9000,
			Network: platform.NetworkConfig{
				CIDR:           "192.168.1.0/24",
				Gateway:        "192.168.1.1",
				BaseHostOffset: 200,
			},
		},
	}
}

func validCredentials() *credentials.Credentials {
	return &credentials.Credentials{ProxmoxToken: "pve-token", AgeKey: "age-key"}
}

// fakePlatform records what bootstrap's platform setup phase asks of
// Gitea, Komodo, and git.
type fakePlatform struct {
	tokens      map[string]string // token name -> password used
	orgs, repos []string
	apiKeys     int
	gitAccounts []string
	regAccounts []string
	synced      []string // remote URLs
}

func (f *fakePlatform) clients() Clients {
	return Clients{
		Gitea:  func(baseURL, token string) GiteaAPI { return &fakeGitea{f} },
		Komodo: func(baseURL, key, secret string) KomodoAPI { return &fakeKomodo{f} },
		SyncRepo: func(ctx context.Context, dir, remoteURL, token, message string) error {
			f.synced = append(f.synced, remoteURL)
			return nil
		},
	}
}

type fakeGitea struct{ f *fakePlatform }

func (g *fakeGitea) WaitReady(ctx context.Context, timeout time.Duration) error { return nil }
func (g *fakeGitea) CreateToken(ctx context.Context, user, password, name string, scopes []string) (string, error) {
	if g.f.tokens == nil {
		g.f.tokens = map[string]string{}
	}
	g.f.tokens[name] = password
	return "token-" + name, nil
}
func (g *fakeGitea) EnsureOrg(ctx context.Context, org string) error {
	g.f.orgs = append(g.f.orgs, org)
	return nil
}
func (g *fakeGitea) EnsureRepo(ctx context.Context, owner, name string) error {
	g.f.repos = append(g.f.repos, owner+"/"+name)
	return nil
}

type fakeKomodo struct{ f *fakePlatform }

func (k *fakeKomodo) WaitReady(ctx context.Context, timeout time.Duration) error { return nil }
func (k *fakeKomodo) Login(ctx context.Context, user, password string) (string, error) {
	return "jwt", nil
}
func (k *fakeKomodo) CreateAPIKey(ctx context.Context, jwt, name string) (string, string, error) {
	k.f.apiKeys++
	return "K_key_K", "S_secret_S", nil
}
func (k *fakeKomodo) EnsureGitProviderAccount(ctx context.Context, domain string, https bool, user, token string) error {
	k.f.gitAccounts = append(k.f.gitAccounts, domain+"|"+token)
	return nil
}
func (k *fakeKomodo) EnsureDockerRegistryAccount(ctx context.Context, domain, user, token string) error {
	k.f.regAccounts = append(k.f.regAccounts, domain+"|"+token)
	return nil
}

// newRunOrchestrator returns an Orchestrator with fake Proxmox, runner,
// and platform clients, and points HOME at a temp dir so generated
// credentials never touch the real ~/.urbex.
func newRunOrchestrator(t *testing.T, proxmox *fakeProxmox) (*Orchestrator, *fakeRunner, *fakePlatform) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runner := &fakeRunner{}
	fp := &fakePlatform{}
	return &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     proxmox,
		Runner:      runner,
		Out:         &bytes.Buffer{},
		Clients:     fp.clients(),
	}, runner, fp
}

func TestRun_DryRun_MaterializesAssetsWithoutExecuting(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir, DryRun: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(runner.calls) != 0 {
		t.Errorf("expected no commands run in dry-run mode, got: %+v", runner.calls)
	}

	for _, want := range []string{
		"terraform/base/main.tf",
		"terraform/modules/lxc/main.tf",
		"terraform/base/terraform.auto.tfvars.json",
		"ansible/playbook.yml",
		"ansible/inventory.yml",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s to be materialized: %v", want, err)
		}
	}
}

func TestRun_ExecutesTerraformThenAnsible(t *testing.T) {
	dir := t.TempDir()
	o, runner, _ := newRunOrchestrator(t, &fakeProxmox{})

	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("expected 3 commands (terraform init, terraform apply, ansible-playbook), got %d: %+v", len(runner.calls), runner.calls)
	}

	want := [][2]string{
		{"terraform", "init"},
		{"terraform", "apply"},
		{"ansible-playbook", "-i"},
	}
	for i, w := range want {
		if runner.calls[i].Name != w[0] || runner.calls[i].Args[0] != w[1] {
			t.Errorf("call %d = %s %v, want starting with %s %s", i, runner.calls[i].Name, runner.calls[i].Args, w[0], w[1])
		}
	}

	foundToken := false
	for _, e := range runner.calls[0].Env {
		if e == "TF_VAR_proxmox_api_token=pve-token" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Errorf("expected TF_VAR_proxmox_api_token in terraform init env, got: %v", runner.calls[0].Env)
	}
}

func TestRun_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: &credentials.Credentials{},
		Proxmox:     &fakeProxmox{},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

	err := o.Run(context.Background(), Options{GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected no commands run when credentials are missing, got: %+v", runner.calls)
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      &platform.Config{},
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

	err := o.Run(context.Background(), Options{GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for an invalid platform config")
	}
}

func TestRun_ExistingUntrackedContainerAborts(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{hostnames: []string{"urbex-gitea"}},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

	err := o.Run(context.Background(), Options{GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error when a base-service hostname exists without local terraform state")
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected no commands run when aborting on an untracked existing container, got: %+v", runner.calls)
	}
}

func TestRun_ExistingTrackedContainerProceeds(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "terraform", "base")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "terraform.tfstate"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	o, runner, _ := newRunOrchestrator(t, &fakeProxmox{hostnames: []string{"urbex-gitea"}})

	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Errorf("expected provisioning to proceed when the existing container is already tracked, got %d calls", len(runner.calls))
	}
}

func TestRun_ProxmoxCheckFails(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{err: errProxmoxUnreachable},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

	err := o.Run(context.Background(), Options{GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error when the Proxmox pre-flight check fails")
	}
}

func TestRun_SetsUpGiteaAndKomodoAndPushesGitOpsRepo(t *testing.T) {
	dir := t.TempDir()
	o, runner, fp := newRunOrchestrator(t, &fakeProxmox{})

	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	creds := o.Credentials
	if creds.GiteaAdminPassword == "" || creds.KomodoDBPassword == "" || creds.KeycloakAdminPassword == "" {
		t.Errorf("expected platform secrets to be generated, got %+v", creds)
	}
	ansible := runner.calls[2]
	env := strings.Join(ansible.Env, "\n")
	for _, want := range []string{"URBEX_GITEA_ADMIN_PASSWORD=" + creds.GiteaAdminPassword, "URBEX_KOMODO_JWT_SECRET=" + creds.KomodoJWTSecret} {
		if !strings.Contains(env, want) {
			t.Errorf("expected %q in ansible env", want)
		}
	}
	if strings.Contains(env, "pve-token") {
		t.Error("the proxmox token must not be passed to ansible")
	}

	if fp.tokens["urbex-cli"] != creds.GiteaAdminPassword || fp.tokens["urbex-pull"] == "" {
		t.Errorf("expected cli and pull tokens created with the admin password, got %v", fp.tokens)
	}
	if creds.GiteaToken != "token-urbex-cli" || creds.RegistryPullToken != "token-urbex-pull" {
		t.Errorf("tokens not stored in credentials: %+v", creds)
	}
	if creds.KomodoAPIKey != "K_key_K" || creds.KomodoAPISecret != "S_secret_S" {
		t.Errorf("komodo api key not stored: %+v", creds)
	}
	if strings.Join(fp.repos, ",") != "urbex/gitops" {
		t.Errorf("expected the gitops repo in the default org, got %v", fp.repos)
	}
	// Gitea is the first base service: .200 in validConfig's network.
	if len(fp.regAccounts) != 1 || fp.regAccounts[0] != "192.168.1.200:3000|token-urbex-cli" {
		t.Errorf("unexpected registry accounts %v", fp.regAccounts)
	}
	if len(fp.gitAccounts) != 1 || fp.gitAccounts[0] != "192.168.1.200:3000|token-urbex-cli" {
		t.Errorf("unexpected git provider accounts %v", fp.gitAccounts)
	}
	if strings.Join(fp.synced, ",") != "http://192.168.1.200:3000/urbex/gitops.git" {
		t.Errorf("unexpected gitops push targets %v", fp.synced)
	}

	inventory, err := os.ReadFile(filepath.Join(dir, "ansible", "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory), "gitea_registry: 192.168.1.200:3000") {
		t.Errorf("expected gitea_registry in inventory vars:\n%s", inventory)
	}
	if strings.Contains(string(inventory), creds.GiteaAdminPassword) {
		t.Error("secrets must never be written to the inventory")
	}

	// Re-running keeps existing secrets and tokens instead of rotating them.
	password, token := creds.GiteaAdminPassword, creds.GiteaToken
	fp.tokens, fp.apiKeys = nil, 0
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if creds.GiteaAdminPassword != password || creds.GiteaToken != token || len(fp.tokens) != 0 || fp.apiKeys != 0 {
		t.Errorf("expected no secret/token rotation on re-run (tokens=%v apiKeys=%d)", fp.tokens, fp.apiKeys)
	}
}
