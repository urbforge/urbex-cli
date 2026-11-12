package proxmoxapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

// The VM urbex's VM template is built from: the cloud image imported as
// its disk, cloud-init with root's key, in the pool, no package upgrade.
func TestCreateVM(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /nodes/pve/qemu" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()

	err := New(server.URL, "t", false).CreateVM(context.Background(), "pve", VMSpec{
		VMID: 9080, Name: "urbex-vm-template", Image: "local:import/debian-13-genericcloud-amd64.qcow2",
		Storage: "local-zfs", Pool: "urbex", Bridge: "vmbr0", IPCIDR: "192.168.1.208/24", Gateway: "192.168.1.1",
		Nameservers: []string{"192.168.1.1"}, SSHPublicKey: "ssh-ed25519 AAAA+b/c urbex",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"vmid":      "9080",
		"name":      "urbex-vm-template",
		"scsi0":     "local-zfs:0,import-from=local:import/debian-13-genericcloud-amd64.qcow2,discard=on",
		"ide2":      "local-zfs:cloudinit",
		"pool":      "urbex",
		"ciuser":    "root",
		"sshkeys":   "ssh-ed25519%20AAAA%2Bb%2Fc%20urbex",
		"ipconfig0": "ip=192.168.1.208/24,gw=192.168.1.1",
		"ciupgrade": "0",
		"agent":     "enabled=1",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// VM templates and VMs are read from the qemu endpoints, not lxc's.
func TestVMTemplatesAndVM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/pve/qemu":
			w.Write([]byte(`{"data":[{"vmid":9080,"template":1},{"vmid":9081},{"vmid":9102,"template":1}]}`))
		case "/nodes/pve/qemu/9080/config":
			w.Write([]byte(`{"data":{"template":1,"description":"urbex VM template, version abc\n"}}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c, ctx := New(server.URL, "t", false), context.Background()
	ids, err := c.VMTemplates(ctx, "pve", 9080, 9089)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int{9080}) {
		t.Errorf("VMTemplates = %v, want [9080]", ids)
	}
	info, err := c.VM(ctx, "pve", 9080)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || !info.Template || info.Description != "urbex VM template, version abc" {
		t.Errorf("VM = %+v", info)
	}
	if info, err := c.VM(ctx, "pve", 9085); err != nil || info != nil {
		t.Errorf("VM of a missing VMID = %+v, %v; want nil", info, err)
	}
}
