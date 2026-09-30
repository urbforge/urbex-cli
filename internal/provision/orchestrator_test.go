package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/rollout"
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
			LXCTemplate:  "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst",
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
		ProxmoxToken:        "pve-token",
		AgeKey:              "age-key",
		GiteaToken:          "gitea-token",
		KomodoAPIKey:        "K_key_K",
		KomodoAPISecret:     "S_secret_S",
		KomodoOnboardingKey: "O_onboard_O",
		KomodoWebhookSecret: "hook-secret",
	}
}

// fakeRollout stands in for internal/rollout, recording what the
// orchestrator asks Komodo for.
type fakeRollout struct {
	setups, runs   int
	serversRemoved int
	removed        []rollout.Target
	runErr         error
}

func (f *fakeRollout) Setup(ctx context.Context, t rollout.Target) (*rollout.Resources, error) {
	f.setups++
	return &rollout.Resources{Procedure: rollout.ProcedureName(t.Project, t.Env), Branch: rollout.Branch(t.Env)}, nil
}

func (f *fakeRollout) Run(ctx context.Context, t rollout.Target) ([]rollout.Deployed, error) {
	f.runs++
	if f.runErr != nil {
		return nil, f.runErr
	}
	var deployed []rollout.Deployed
	for _, svc := range t.Services {
		deployed = append(deployed, rollout.Deployed{
			Service: svc.Name,
			State:   "running",
			Image:   "192.168.1.200:3000/urbex/" + t.Project + "-" + svc.Name + ":0.0.1-" + t.Env,
		})
	}
	return deployed, nil
}

func (f *fakeRollout) Remove(ctx context.Context, t rollout.Target) error {
	f.removed = append(f.removed, t)
	return nil
}

func (f *fakeRollout) RemoveServers(ctx context.Context, t rollout.Target) error {
	f.serversRemoved++
	return nil
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
	}

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error when the service has no allocation yet (apply not run)")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run when unprovisioned, got: %+v", r.calls)
	}
}

func TestDeploy_OnlyTalksToKomodoAfterApply(t *testing.T) {
	dir := t.TempDir()

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	deployRunner := &fakeRunner{}
	o.Runner = deployRunner
	ro := &fakeRollout{}
	o.Rollout = ro

	if err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(deployRunner.calls) != 0 {
		t.Fatalf("expected deploy to run no terraform/ansible at all, got %+v", deployRunner.calls)
	}
	if ro.setups != 1 || ro.runs != 1 {
		t.Errorf("expected deploy to re-sync and run the rollout once, got setups=%d runs=%d", ro.setups, ro.runs)
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
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
		Rollout:     &fakeRollout{},
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}

func TestApply_InstallsPeripheryThenHandsOverToKomodo(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}
	ro := &fakeRollout{}
	var synced []string
	out := &bytes.Buffer{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         out,
		Rollout:     ro,
		SyncGitOps: func(ctx context.Context, path, message string) error {
			synced = append(synced, message)
			return nil
		},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if ro.setups != 1 || ro.runs != 1 {
		t.Errorf("expected one rollout setup and run, got setups=%d runs=%d", ro.setups, ro.runs)
	}
	inventory, err := os.ReadFile(filepath.Join(dir, "ansible", "projects", "my-app", "staging", "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"gitea_registry: 192.168.1.200:3000",
		"komodo_core_address: http://192.168.1.201:9120",
		"komodo_version: 2.2.0",
		"ansible_host: 192.168.1.210",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("expected %q in inventory:\n%s", want, inventory)
		}
	}
	if strings.Contains(string(inventory), "O_onboard_O") || strings.Contains(string(inventory), "docker_image") {
		t.Errorf("the inventory must hold neither secrets nor app details:\n%s", inventory)
	}
	ansible := r.calls[len(r.calls)-1]
	if ansible.Name != "ansible-playbook" || len(ansible.Env) != 1 || ansible.Env[0] != "URBEX_KOMODO_ONBOARDING_KEY=O_onboard_O" {
		t.Errorf("expected ansible last, with only the onboarding key in its env, got %s %v", ansible.Name, ansible.Env)
	}
	if _, err := os.Stat(filepath.Join(dir, "komodo", "my-app", "staging.json")); err != nil {
		t.Errorf("expected the declared komodo resources to be recorded: %v", err)
	}
	if len(synced) != 1 || synced[0] != "urbex apply my-app staging" {
		t.Errorf("unexpected gitops syncs %v", synced)
	}
	for _, want := range []string{"http://192.168.1.210:8080", "my-app-api:0.0.1-staging", "merging into staging deploys staging automatically"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestApply_RolloutFailureStillSyncsGitOpsAndFails(t *testing.T) {
	dir := t.TempDir()
	synced := 0
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{runErr: errors.New("komodo procedure my-app-staging failed")},
		SyncGitOps:  func(ctx context.Context, path, message string) error { synced++; return nil },
	}
	err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "procedure my-app-staging failed") {
		t.Fatalf("expected the rollout failure, got %v", err)
	}
	if synced != 1 {
		t.Errorf("expected the infrastructure state to be synced even though the rollout failed, got %d syncs", synced)
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
		Rollout:     &fakeRollout{},
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

func TestDestroy_RemovesKomodoResourcesBeforeTheLXCs(t *testing.T) {
	dir := t.TempDir()
	ro := &fakeRollout{}
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     ro,
	}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if ro.serversRemoved != 1 {
		t.Errorf("expected the komodo servers to be removed once, got %d", ro.serversRemoved)
	}
	if len(ro.removed) != 1 || ro.removed[0].Env != "staging" || len(ro.removed[0].Services) != 1 {
		t.Errorf("expected the staging rollout to be removed, got %+v", ro.removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "komodo", "my-app", "staging.json")); !os.IsNotExist(err) {
		t.Errorf("expected the komodo resources record to be deleted, stat err = %v", err)
	}
}
