package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/credentials"
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
	return &credentials.Credentials{ProxmoxToken: "pve-token", AgeKey: "age-key"}
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
		"ansible/projects/my-app/staging/inventory.yml",
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
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for a manifest with no services")
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
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}
