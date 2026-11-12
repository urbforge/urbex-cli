package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// vmTemplateProxmox is a fake Proxmox that can build VM templates: vms
// holds the VMs of the VM template range by VMID; inUse are templates
// with clones, which Proxmox won't delete.
type vmTemplateProxmox struct {
	fakeProxmox
	vms   map[int]*proxmoxapi.ContainerInfo
	inUse map[int]bool
	spec  proxmoxapi.VMSpec
	calls []string
}

func (p *vmTemplateProxmox) step(name string, vmid int) error {
	p.calls = append(p.calls, fmt.Sprintf("%s %d", name, vmid))
	return nil
}
func (p *vmTemplateProxmox) VMTemplates(ctx context.Context, node string, from, to int) ([]int, error) {
	var ids []int
	for id := from; id <= to; id++ {
		if v := p.vms[id]; v != nil && v.Template {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (p *vmTemplateProxmox) VM(ctx context.Context, node string, vmid int) (*proxmoxapi.ContainerInfo, error) {
	return p.vms[vmid], nil
}
func (p *vmTemplateProxmox) CreateVM(ctx context.Context, node string, spec proxmoxapi.VMSpec) error {
	p.spec = spec
	if p.vms == nil {
		p.vms = map[int]*proxmoxapi.ContainerInfo{}
	}
	p.vms[spec.VMID] = &proxmoxapi.ContainerInfo{}
	return p.step("create", spec.VMID)
}
func (p *vmTemplateProxmox) ResizeVMDisk(ctx context.Context, node string, vmid int, disk string, sizeGB int) error {
	return p.step(fmt.Sprintf("resize %s %dG", disk, sizeGB), vmid)
}
func (p *vmTemplateProxmox) StartVM(ctx context.Context, node string, vmid int) error {
	return p.step("start", vmid)
}
func (p *vmTemplateProxmox) ShutdownVM(ctx context.Context, node string, vmid, timeout int) error {
	return p.step("shutdown", vmid)
}
func (p *vmTemplateProxmox) StopVM(ctx context.Context, node string, vmid int) error {
	return p.step("stop", vmid)
}
func (p *vmTemplateProxmox) ConvertVMToTemplate(ctx context.Context, node string, vmid int) error {
	p.vms[vmid].Template = true
	return p.step("template", vmid)
}
func (p *vmTemplateProxmox) SetVMDescription(ctx context.Context, node string, vmid int, d string) error {
	p.vms[vmid].Description = d
	return p.step("describe", vmid)
}
func (p *vmTemplateProxmox) DeleteVM(ctx context.Context, node string, vmid int) error {
	if p.inUse[vmid] {
		return errors.New("base volume used by linked clone")
	}
	delete(p.vms, vmid)
	return p.step("delete", vmid)
}

func newVMTemplateBuilder(t *testing.T, p *vmTemplateProxmox) (*VMTemplateBuilder, *fakeRunner) {
	t.Helper()
	cfg := validConfig()
	cfg.Proxmox.Pool = "urbex"
	cfg.Proxmox.VMImage = "local:import/debian-13-genericcloud-amd64.qcow2"
	runner := &fakeRunner{}
	return &VMTemplateBuilder{
		Proxmox: p, Config: cfg, Runner: runner, Out: &bytes.Buffer{},
		WaitSSH: func(context.Context, string) error { return nil },
	}, runner
}

// The VM template is built from the cloud image at the first VMID of its
// range, prepared by the template playbook, and turned into a template
// marked with its version (ADR-0033).
func TestVMTemplate_BuiltWhenMissing(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ansible"), 0o755)
	p := &vmTemplateProxmox{}
	b, runner := newVMTemplateBuilder(t, p)

	vmid, err := b.Ensure(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if vmid != 9080 {
		t.Errorf("vmid = %d, want 9080 (vmIdBase+80)", vmid)
	}
	if got := strings.Join(p.calls, ","); got != "create 9080,resize scsi0 5G 9080,start 9080,shutdown 9080,template 9080,describe 9080" {
		t.Errorf("proxmox calls = %s", got)
	}
	if p.spec.Name != VMTemplateHostname || p.spec.Image != b.Config.Proxmox.VMImage || p.spec.Pool != "urbex" || !strings.HasPrefix(p.spec.IPCIDR, "192.168.1.208/") {
		t.Errorf("spec = %+v", p.spec)
	}
	if len(runner.calls) != 1 || runner.calls[0].Args[len(runner.calls[0].Args)-1] != "template-playbook.yml" {
		t.Errorf("runner calls = %+v", runner.calls)
	}
	inv, _ := os.ReadFile(filepath.Join(dir, "ansible", templateInventory))
	if !strings.Contains(string(inv), "urbex_vm_template:") || !strings.Contains(string(inv), VMTemplateHostname) {
		t.Errorf("inventory = %s", inv)
	}
	if !strings.Contains(p.vms[9080].Description, "version "+VMTemplateVersion(b.Config)) {
		t.Errorf("description = %q", p.vms[9080].Description)
	}
}

// The current template is used as it is; an older one goes, unless VMs
// are still clones of it.
func TestVMTemplate_CurrentKeptOlderRemoved(t *testing.T) {
	p := &vmTemplateProxmox{}
	b, runner := newVMTemplateBuilder(t, p)
	p.vms = map[int]*proxmoxapi.ContainerInfo{
		9080: {Template: true, Description: vmTemplateDescription("old")},
		9081: {Template: true, Description: vmTemplateDescription("older")},
		9082: {Template: true, Description: vmTemplateDescription(VMTemplateVersion(b.Config))},
	}
	p.inUse = map[int]bool{9081: true}

	vmid, err := b.Ensure(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if vmid != 9082 || len(runner.calls) != 0 {
		t.Errorf("vmid = %d, runs = %d: want the current template, nothing built", vmid, len(runner.calls))
	}
	if p.vms[9080] != nil || p.vms[9081] == nil {
		t.Errorf("want 9080 removed and 9081 (with clones) kept, have %v", p.vms)
	}
}

// What an interrupted build left in the range goes before a new one.
func TestVMTemplate_UnfinishedBuildRemoved(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ansible"), 0o755)
	p := &vmTemplateProxmox{vms: map[int]*proxmoxapi.ContainerInfo{9080: {}}}
	b, _ := newVMTemplateBuilder(t, p)
	if _, err := b.Ensure(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Join(p.calls, ","), "stop 9080,delete 9080,create 9080,") {
		t.Errorf("proxmox calls = %v", p.calls)
	}
}

// Without proxmox.vmImage there is nothing to build a VM from: apply
// stops and says how to add it.
func TestVMTemplate_NeedsTheImage(t *testing.T) {
	p := &vmTemplateProxmox{}
	b, _ := newVMTemplateBuilder(t, p)
	b.Config.Proxmox.VMImage = ""
	_, err := b.Ensure(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "proxmox.vmImage") {
		t.Fatalf("err = %v, want one naming proxmox.vmImage", err)
	}
	if len(p.calls) != 0 {
		t.Errorf("proxmox calls = %v, want none", p.calls)
	}
}

// Another image makes another template.
func TestVMTemplateVersion_FollowsTheImage(t *testing.T) {
	cfg := validConfig()
	cfg.Proxmox.VMImage = "local:import/a.qcow2"
	a := VMTemplateVersion(cfg)
	cfg.Proxmox.VMImage = "local:import/b.qcow2"
	if a == VMTemplateVersion(cfg) {
		t.Error("the version must change with the image")
	}
	if a == TemplateVersion(cfg) {
		t.Error("the VM and LXC templates must have different versions")
	}
}

// Teardown removes the VM templates, and an unfinished build: the VMs
// cloned from them are gone by then.
func TestRemoveVMTemplate(t *testing.T) {
	p := &vmTemplateProxmox{vms: map[int]*proxmoxapi.ContainerInfo{9080: {Template: true}, 9083: {}}}
	o := &Orchestrator{Config: validConfig(), Proxmox: p}
	if err := o.removeVMTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.calls, ","); got != "delete 9080,stop 9083,delete 9083" {
		t.Errorf("proxmox calls = %s", got)
	}
}
