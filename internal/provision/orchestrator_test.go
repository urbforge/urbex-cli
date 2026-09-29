package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/imagebuild"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/runner"
)

type fakeRunner struct {
	calls []runner.Command
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, c runner.Command) error {
	f.calls = append(f.calls, c)
	return f.err
}

func validPlatformConfig() *platform.Config {
	return &platform.Config{
		Domain: "example.com",
		Git:    platform.GitConfig{GiteaURL: "https://git.example.com"},
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

func validManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: "urbex/v1",
		Project:    "my-app",
		Services: []manifest.Service{
			{Name: "api", Runtime: "go", Port: 8080},
		},
	}
}

func validCredentials() *credentials.Credentials {
	return &credentials.Credentials{
		ProxmoxToken:      "pve-token",
		AgeKey:            "age-key",
		GiteaToken:        "gitea-token",
		RegistryPullToken: "pull-token",
		KomodoAPIKey:      "K_key_K",
		KomodoAPISecret:   "S_secret_S",
	}
}

// fakeImages stands in for internal/imagebuild: every service gets an
// image tagged with a fixed commit.
type fakeImages struct{ builds int }

func (f *fakeImages) Build(ctx context.Context, project string, services []manifest.Service) (*imagebuild.Release, error) {
	f.builds++
	images := map[string]string{}
	for _, svc := range services {
		images[svc.Name] = "192.168.1.200:3000/urbex/" + project + "-" + svc.Name + ":abc1234"
	}
	return &imagebuild.Release{Commit: "abc1234def", Images: images}, nil
}

func TestPlan_RunsTerraformInitAndPlanOnly(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if len(r.calls) != 2 {
		t.Fatalf("expected 2 commands (init, plan), got %d: %+v", len(r.calls), r.calls)
	}
	if r.calls[0].Args[0] != "init" || r.calls[1].Args[0] != "plan" {
		t.Errorf("unexpected command sequence: %+v", r.calls)
	}

	for _, want := range []string{
		"terraform/projects/my-app/staging/main.tf",
		"terraform/projects/my-app/staging/terraform.auto.tfvars.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s to be materialized: %v", want, err)
		}
	}
}

func TestApply_RunsTerraformThenAnsible(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(r.calls) != 3 {
		t.Fatalf("expected 3 commands (init, apply, ansible-playbook), got %d: %+v", len(r.calls), r.calls)
	}
	if r.calls[0].Args[0] != "init" || r.calls[1].Args[0] != "apply" || r.calls[2].Name != "ansible-playbook" {
		t.Errorf("unexpected command sequence: %+v", r.calls)
	}
}

func TestPrepare_AllocatesAndPersistsIPsAndVMIDs(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	tfvarsPath := filepath.Join(dir, "terraform", "projects", "my-app", "staging", "terraform.auto.tfvars.json")
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		t.Fatalf("reading tfvars: %v", err)
	}
	var parsed tfVars
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshaling tfvars: %v", err)
	}

	svc, ok := parsed.Services["my-app-api-staging"]
	if !ok {
		t.Fatalf("expected hostname my-app-api-staging in services, got: %+v", parsed.Services)
	}
	// BaseHostOffset 200 -> project allocations start at .210 (see prepare()).
	if svc.IPAddress != "192.168.1.210" {
		t.Errorf("IPAddress = %q, want 192.168.1.210", svc.IPAddress)
	}
	if svc.VMID != 9100 {
		t.Errorf("VMID = %d, want 9100 (VMIDBase 9000 + 100)", svc.VMID)
	}
	if svc.Cores != manifest.DefaultCPU {
		t.Errorf("Cores = %d, want default %d", svc.Cores, manifest.DefaultCPU)
	}

	// Re-running Plan must reuse the same allocation, not hand out a new one.
	r2 := &fakeRunner{}
	o.Runner = r2
	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	data2, err := os.ReadFile(tfvarsPath)
	if err != nil {
		t.Fatalf("reading tfvars after second plan: %v", err)
	}
	var parsed2 tfVars
	if err := json.Unmarshal(data2, &parsed2); err != nil {
		t.Fatalf("unmarshaling tfvars after second plan: %v", err)
	}
	if parsed2.Services["my-app-api-staging"] != svc {
		t.Errorf("expected the same allocation on re-plan, got %+v then %+v", svc, parsed2.Services["my-app-api-staging"])
	}
}

func TestPlan_InvalidEnv(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	err := o.Plan(context.Background(), Options{Env: "development", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for an invalid environment")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for an invalid environment, got: %+v", r.calls)
	}
}

func TestPlan_NoServices(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	m := validManifest()
	m.Services = nil

	o := &Orchestrator{
		Manifest:    m,
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for a manifest with no services")
	}
}

func TestDeploy_RequiresPriorApply(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error when the service has no allocation yet (apply not run)")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run when unprovisioned, got: %+v", r.calls)
	}
}

func TestDeploy_RunsAnsibleOnlyAfterApply(t *testing.T) {
	dir := t.TempDir()

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	deployRunner := &fakeRunner{}
	o.Runner = deployRunner

	if err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(deployRunner.calls) != 1 {
		t.Fatalf("expected exactly 1 command (ansible-playbook, no terraform), got %d: %+v", len(deployRunner.calls), deployRunner.calls)
	}
	if deployRunner.calls[0].Name != "ansible-playbook" {
		t.Errorf("expected ansible-playbook, got %s", deployRunner.calls[0].Name)
	}
}

func TestDeploy_PartiallyProvisionedFailsListingAllMissing(t *testing.T) {
	dir := t.TempDir()

	m := validManifest()
	m.Services = append(m.Services, manifest.Service{Name: "worker", Runtime: "go", Port: 9000})

	o := &Orchestrator{
		Manifest:    m,
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	// Provision only the first service via Plan+manual ledger write is
	// overkill here; instead apply the full manifest, then deploy a
	// manifest that adds a third, never-applied service.
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	o.Manifest.Services = append(o.Manifest.Services, manifest.Service{Name: "never-applied", Runtime: "go", Port: 9001})
	deployRunner := &fakeRunner{}
	o.Runner = deployRunner

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error listing the unprovisioned service")
	}
	if len(deployRunner.calls) != 0 {
		t.Errorf("expected no commands run when any service is unprovisioned, got: %+v", deployRunner.calls)
	}
}

func TestDestroy_NothingAppliedIsANoOp(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run when nothing was applied, got: %+v", r.calls)
	}
}

func TestDestroy_RunsTerraformDestroyAndClearsLedger(t *testing.T) {
	dir := t.TempDir()

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ledgerPath := filepath.Join(dir, "state", "allocations.json")
	before, err := allocation.Load(ledgerPath, 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := before.Lookup("my-app-api-staging"); !ok {
		t.Fatal("expected an allocation to exist after Apply")
	}

	destroyRunner := &fakeRunner{}
	o.Runner = destroyRunner

	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(destroyRunner.calls) != 2 {
		t.Fatalf("expected 2 commands (terraform init, terraform destroy), got %d: %+v", len(destroyRunner.calls), destroyRunner.calls)
	}
	if destroyRunner.calls[0].Args[0] != "init" || destroyRunner.calls[1].Args[0] != "destroy" {
		t.Errorf("unexpected command sequence: %+v", destroyRunner.calls)
	}

	after, err := allocation.Load(ledgerPath, 9100, 210)
	if err != nil {
		t.Fatalf("Load (after destroy): %v", err)
	}
	if _, ok := after.Lookup("my-app-api-staging"); ok {
		t.Error("expected the allocation to be removed after Destroy")
	}
}

func TestDestroy_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: &credentials.Credentials{},
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}

func TestPlan_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: &credentials.Credentials{},
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}

func TestApply_DeploysBuiltImagesAndSyncsGitOps(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}
	images := &fakeImages{}
	var synced []string

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Images:      images,
		SyncGitOps: func(ctx context.Context, path, message string) error {
			synced = append(synced, message)
			return nil
		},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if images.builds != 1 {
		t.Errorf("expected one image build, got %d", images.builds)
	}
	inventory, err := os.ReadFile(filepath.Join(dir, "ansible", "projects", "my-app", "staging", "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"docker_image: 192.168.1.200:3000/urbex/my-app-api:abc1234",
		"gitea_registry: 192.168.1.200:3000",
		"registry_username: urbex-admin",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("expected %q in inventory:\n%s", want, inventory)
		}
	}
	if strings.Contains(string(inventory), "pull-token") {
		t.Error("the registry pull token must not be written to the inventory")
	}
	ansible := r.calls[len(r.calls)-1]
	if len(ansible.Env) != 1 || ansible.Env[0] != "URBEX_REGISTRY_PULL_TOKEN=pull-token" {
		t.Errorf("expected only the pull token in ansible env, got %v", ansible.Env)
	}
	if len(synced) != 1 || synced[0] != "urbex apply my-app staging @ abc1234" {
		t.Errorf("unexpected gitops syncs %v", synced)
	}
}

func TestDeploy_RequiresBootstrappedPlatformCredentials(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Images:      &fakeImages{},
	}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	o.Credentials.KomodoAPIKey = ""
	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "URBEX_KOMODO_API_KEY") {
		t.Errorf("expected a missing platform credential error, got %v", err)
	}
}
