package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

func validCredentials() *credentials.Credentials {
	return &credentials.Credentials{ProxmoxToken: "pve-token", AgeKey: "age-key"}
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
	runner := &fakeRunner{}

	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

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

	runner := &fakeRunner{}
	o := &Orchestrator{
		Config:      validConfig(),
		Credentials: validCredentials(),
		Proxmox:     &fakeProxmox{hostnames: []string{"urbex-gitea"}},
		Runner:      runner,
		Out:         &bytes.Buffer{},
	}

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
