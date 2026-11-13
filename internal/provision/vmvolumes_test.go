package provision

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// fakeVMVolumes adds a VM's disks to fakeVolumes: the VMs by VMID, with
// their SCSI disks.
type fakeVMVolumes struct {
	*fakeVolumes
	disks map[int][]proxmoxapi.VMDisk
}

func (f *fakeVMVolumes) VM(_ context.Context, _ string, vmid int) (*proxmoxapi.ContainerInfo, error) {
	if _, ok := f.disks[vmid]; !ok {
		return nil, nil
	}
	return &proxmoxapi.ContainerInfo{}, nil
}

func (f *fakeVMVolumes) VMDisks(_ context.Context, _ string, vmid int) ([]proxmoxapi.VMDisk, error) {
	return slices.Clone(f.disks[vmid]), nil
}

func (f *fakeVMVolumes) AddVMDisk(_ context.Context, _ string, vmid int, key, storage string, sizeGB int, serial string) error {
	f.calls = append(f.calls, fmt.Sprintf("disk %d %s %s:%d %s", vmid, key, storage, sizeGB, serial))
	f.disks[vmid] = append(f.disks[vmid], proxmoxapi.VMDisk{Key: key, Serial: serial, SizeGB: sizeGB})
	return nil
}

func (f *fakeVMVolumes) ResizeVMDisk(_ context.Context, _ string, vmid int, key string, sizeGB int) error {
	f.calls = append(f.calls, fmt.Sprintf("grow %d %s %d", vmid, key, sizeGB))
	for i, d := range f.disks[vmid] {
		if d.Key == key {
			f.disks[vmid][i].SizeGB = sizeGB
		}
	}
	return nil
}

// vmOrchestrator is the media project with vault in a VM.
func vmOrchestrator(t *testing.T) (*Orchestrator, *fakeVMVolumes, *strings.Builder) {
	t.Helper()
	o, vols, _, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	o.Manifest.Services[0].Machine = manifest.MachineVM
	vv := &fakeVMVolumes{fakeVolumes: vols, disks: map[int][]proxmoxapi.VMDisk{}}
	o.Volumes = vv
	return o, vv, out
}

// A VM's named volume is a disk with the volume's serial, plugged in at
// once: no restart, no mount point; Ansible gets the disk to mount.
func TestApply_VMNamedVolumesAreDisks(t *testing.T) {
	dir := t.TempDir()
	o, vv, out := vmOrchestrator(t)
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out)
	}
	vmid := vmidOf(t, o, dir, "vault")
	if want := []string{fmt.Sprintf("disk %d scsi1 local-lvm:5 vault-data", vmid)}; !slices.Equal(vv.calls, want) {
		t.Errorf("calls = %v, want %v", vv.calls, want)
	}
	if len(vv.reboots) != 0 {
		t.Errorf("a VM's disk needs no restart: %v", vv.reboots)
	}
	inv, _ := os.ReadFile(filepath.Join(dir, "ansible", "projects", "media", "prod", "inventory.yml"))
	if !strings.Contains(string(inv), "serial: vault-data") || !strings.Contains(string(inv), "path: /srv/volumes/vault/data") {
		t.Errorf("inventory lacks the VM's volume:\n%s", inv)
	}

	// Again: nothing to do. Bigger: the disk grows. Smaller: refused.
	vv.calls = nil
	o.Proxmox = fakeLister{vmid}
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil || len(vv.calls) != 0 {
		t.Fatalf("re-apply: %v, calls %v", err, vv.calls)
	}
	o.Manifest.Services[0].Volumes[0].Size = "8Gi"
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil || !slices.Equal(vv.calls, []string{fmt.Sprintf("grow %d scsi1 8", vmid)}) {
		t.Fatalf("grow: %v, calls %v", err, vv.calls)
	}
	o.Manifest.Services[0].Volumes[0].Size = "2Gi"
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err == nil || !strings.Contains(err.Error(), "can't shrink") {
		t.Errorf("shrink: %v", err)
	}
}

// Destroying a VM with named volumes needs --delete-data, like an LXC's:
// its disks go with it. A disk of a volume no longer declared counts.
func TestDestroy_VMNamedVolumesNeedDeleteData(t *testing.T) {
	dir := t.TempDir()
	o, vv, out := vmOrchestrator(t)
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	vmid := vmidOf(t, o, dir, "vault")
	vv.disks[vmid] = append([]proxmoxapi.VMDisk{{Key: "scsi0", SizeGB: 8}}, append(vv.disks[vmid], proxmoxapi.VMDisk{Key: "scsi2", Serial: "vault-old", SizeGB: 1})...)
	o.Proxmox = fakeLister{vmid}
	o.Runner = &fakeRunner{}

	err := o.Destroy(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "vault/data, vault/old") {
		t.Fatalf("expected destroy refused naming the volumes, got %v", err)
	}
	if err := o.Destroy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, DeleteData: true}); err != nil {
		t.Fatalf("Destroy --delete-data: %v\n%s", err, out)
	}
}
