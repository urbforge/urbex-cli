package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
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
