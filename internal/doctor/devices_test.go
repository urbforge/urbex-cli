package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

type fakeDevices map[int][]proxmoxapi.Device

func (f fakeDevices) Devices(_ context.Context, _ string, vmid int) ([]proxmoxapi.Device, error) {
	if vmid == 666 {
		return nil, errors.New("500 no such container")
	}
	return f[vmid], nil
}

func TestDevicesChecks(t *testing.T) {
	d := fakeDevices{
		9101: {{Key: "dev0", Path: "/dev/net/tun"}},
		9102: {{Key: "dev0", Path: "/dev/fuse"}},
	}
	machines := []DeviceMachine{
		{Service: "gluetun", Env: "prod", VMID: 9101, Devices: []string{"/dev/net/tun"}},
		{Service: "gluetun", Env: "staging", VMID: 9102, Devices: []string{"/dev/net/tun", "/dev/dri/renderD128"}},
		{Service: "jellyfin", Env: "prod", Devices: []string{"/dev/dri/renderD128"}},
		{Service: "vpn", Env: "prod", VMID: 9104, VM: true, Devices: []string{"/dev/net/tun"}},
		{Service: "gone", Env: "prod", VMID: 666, Devices: []string{"/dev/net/tun"}},
	}
	r := &Report{}
	DevicesChecks(context.Background(), d, "pve", machines, r)
	want := []struct{ name, status, in string }{
		{"gluetun (prod)", OK, "bound to LXC 9101"},
		{"gluetun (staging)", Fail, "pct set 9102 -dev1 /dev/net/tun; pct set 9102 -dev2 /dev/dri/renderD128"},
		{"jellyfin (prod)", Skip, "urbex apply prod"},
		{"vpn (prod)", OK, "a VM has its own"},
		{"gone (prod)", Warn, "no such container"},
	}
	if len(r.Checks) != len(want) {
		t.Fatalf("checks = %+v", r.Checks)
	}
	for i, w := range want {
		c := r.Checks[i]
		if c.Group != "devices" || c.Name != w.name || c.Status != w.status || !strings.Contains(c.Detail+" "+c.Fix, w.in) {
			t.Errorf("check %d = %+v, want %s %s with %q", i, c, w.name, w.status, w.in)
		}
	}
	if !r.Failed() {
		t.Error("a missing device fails the report")
	}

	r = &Report{}
	DevicesChecks(context.Background(), nil, "pve", machines[:1], r)
	if len(r.Checks) != 1 || r.Checks[0].Status != Skip {
		t.Errorf("without the API: %+v", r.Checks)
	}
}
