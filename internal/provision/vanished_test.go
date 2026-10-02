package provision

import (
	"bytes"
	"context"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeLister []int

func (f fakeLister) ListContainerIDs(context.Context, string) ([]int, error) { return f, nil }

const twoContainersState = `{
  "version": 4,
  "resources": [
    {"module": "module.service[\"hello-api-staging\"]", "mode": "managed",
     "type": "proxmox_virtual_environment_container", "name": "this",
     "instances": [{"attributes": {"vm_id": 9102}}]},
    {"module": "module.service[\"hello-web-staging\"]", "mode": "managed",
     "type": "proxmox_virtual_environment_container", "name": "this",
     "instances": [{"attributes": {"vm_id": 9103}}]},
    {"mode": "data", "type": "proxmox_virtual_environment_container", "name": "x",
     "instances": [{"attributes": {"vm_id": 1}}]}
  ]
}`

// A pool-scoped token gets 403, not 404, for a deleted LXC, so Terraform
// can't find out by itself: urbex drops the vanished ones from the state,
// and only those.
func TestForgetVanished_DropsOnlyLXCsGoneFromProxmox(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(twoContainersState), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	o := &Orchestrator{Config: validPlatformConfig(), Runner: r, Out: &bytes.Buffer{}, Proxmox: fakeLister{9000, 9103}}
	if err := o.forgetVanished(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected one terraform call, got %+v", r.calls)
	}
	want := []string{"state", "rm", `module.service["hello-api-staging"].proxmox_virtual_environment_container.this`}
	if !reflect.DeepEqual(r.calls[0].Args, want) {
		t.Errorf("args = %q, want %q", r.calls[0].Args, want)
	}
}

func TestForgetVanished_NoStateOrNoClientIsANoOp(t *testing.T) {
	r := &fakeRunner{}
	o := &Orchestrator{Config: validPlatformConfig(), Runner: r, Out: &bytes.Buffer{}, Proxmox: fakeLister{}}
	if err := o.forgetVanished(context.Background(), t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	o.Proxmox = nil
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(twoContainersState), 0o644)
	if err := o.forgetVanished(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no terraform calls, got %+v", r.calls)
	}
}

// An LXC terraform creates has a new host key, possibly at an address an
// older LXC had: Apply forgets the old key, and keeps those of LXCs that
// were already there.
func TestApply_ForgetsHostKeysOfNewLXCsOnly(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
		Proxmox:     fakeLister{},
	}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	known := filepath.Join(dir, "state", "known_hosts")
	stale := "192.168.1.210 ssh-ed25519 AAAAstale\n9.9.9.9 ssh-ed25519 AAAAother\n"
	os.WriteFile(known, []byte(stale), 0o644)

	// The LXC exists now: re-applying keeps its key.
	o.Proxmox = fakeLister{9100}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := os.ReadFile(known); string(got) != stale {
		t.Errorf("an existing LXC's host key must be kept, known_hosts:\n%s", got)
	}

	// It was deleted outside urbex: the next apply creates a new one.
	o.Proxmox = fakeLister{}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := os.ReadFile(known); string(got) != "9.9.9.9 ssh-ed25519 AAAAother\n" {
		t.Errorf("the recreated LXC's old host key should be gone, known_hosts:\n%s", got)
	}
}

// Every project LXC's log agent labels the logs with the project, the
// service and the environment, and sends them to the platform's Loki -
// unless the manifest turns logs off.
func TestApply_InventoryShipsLogsToLoki(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{Manifest: validManifest(), Config: validPlatformConfig(), Credentials: validCredentials(),
		Runner: &fakeRunner{}, Out: &bytes.Buffer{}, Rollout: &fakeRollout{}}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	inventory, _ := os.ReadFile(filepath.Join(dir, "ansible", "projects", "my-app", "staging", "inventory.yml"))
	for _, want := range []string{
		`urbex_logs: "true"`, "loki_push_url: http://192.168.1.204:3100/loki/api/v1/push",
		"urbex_project: my-app", "urbex_service: api", "urbex_env: staging",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("inventory lacks %q:\n%s", want, inventory)
		}
	}

	off := false
	o.Manifest.Observability = &manifest.Observability{Logs: &off}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	inventory, _ = os.ReadFile(filepath.Join(dir, "ansible", "projects", "my-app", "staging", "inventory.yml"))
	if !strings.Contains(string(inventory), `urbex_logs: "false"`) {
		t.Errorf("observability.logs: false must turn the agent off:\n%s", inventory)
	}
}
