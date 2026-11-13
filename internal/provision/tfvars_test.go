package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
)

func TestStartupOrder_EachLXCAfterWhatItUses(t *testing.T) {
	m := &manifest.Manifest{Services: []manifest.Service{
		{Name: "db", Provides: manifest.InterfacePostgres, ReadyWithin: "10m"},
		{Name: "api", Port: 80, Healthcheck: "/", ReadyWithin: "90s", Uses: []string{"db"}},
		{Name: "web", Port: 80, Uses: []string{"api", "db"}},
		{Name: "alone", Port: 80},
	}}
	want := map[string][2]int{
		"db":    {10, 300}, // ready_within capped
		"api":   {11, 90},
		"web":   {12, 0}, // nothing uses it: nothing to hold back
		"alone": {10, 0},
	}
	for _, svc := range m.Services {
		order, delay := startupOrder(m, svc)
		if got := [2]int{order, delay}; got != want[svc.Name] {
			t.Errorf("startupOrder(%s) = %v, want %v", svc.Name, got, want[svc.Name])
		}
	}
}

// The applications with machine: vm go in vms, cloned from the VM
// template, with a VM's default disk (ADR-0033); the others stay LXCs.
func TestWriteTFVars_VMsApart(t *testing.T) {
	dir := t.TempDir()
	cfg := &platform.Config{Proxmox: platform.ProxmoxConfig{Node: "pve", StoragePool: "local-zfs"}}
	services := []manifest.Service{
		{Name: "api", Port: 80},
		{Name: "backup", Machine: manifest.MachineVM},
	}
	allocations := map[string]allocation.Entry{
		"hello-api-prod":    {VMID: 9100, IP: "192.168.1.210"},
		"hello-backup-prod": {VMID: 9101, IP: "192.168.1.211"},
	}
	if err := writeTFVars(dir, cfg, "hello", "prod", services, allocations, 24, nil, 9090, 9080); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "terraform.auto.tfvars.json"))
	var got tfVars
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Services["hello-api-prod"]; !ok || len(got.Services) != 1 {
		t.Errorf("services = %v, want only the LXC", got.Services)
	}
	vm, ok := got.VMs["hello-backup-prod"]
	if !ok || len(got.VMs) != 1 || vm.VMID != 9101 || vm.DiskGB != 8 || vm.MemoryMB != 1024 {
		t.Errorf("vms = %+v, want the VM with an 8 GB disk", got.VMs)
	}
	if got.CloneVMID != 9090 || got.VMCloneVMID != 9080 {
		t.Errorf("clone_vm_id = %d, vm_clone_vm_id = %d", got.CloneVMID, got.VMCloneVMID)
	}
}
