package tfstate

import (
	"reflect"
	"testing"
)

// The state's LXCs and VMs (ADR-0033) are both machines Proxmox may have
// lost; nothing else in it is.
func TestContainers_LXCsAndVMs(t *testing.T) {
	got, err := Containers([]byte(`{"version":4,"resources":[
		{"module":"module.service[\"hello-api-prod\"]","mode":"managed","type":"proxmox_virtual_environment_container","name":"this","instances":[{"attributes":{"vm_id":9100}}]},
		{"module":"module.vm[\"hello-backup-prod\"]","mode":"managed","type":"proxmox_virtual_environment_vm","name":"this","instances":[{"attributes":{"vm_id":9101}}]},
		{"mode":"data","type":"proxmox_virtual_environment_vm","name":"other","instances":[{"attributes":{"vm_id":1}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Container{
		{Address: `module.service["hello-api-prod"].proxmox_virtual_environment_container.this`, VMID: 9100},
		{Address: `module.vm["hello-backup-prod"].proxmox_virtual_environment_vm.this`, VMID: 9101},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Containers = %+v, want %+v", got, want)
	}
}
