package provision

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// fakeDevices adds an LXC's device files to fakeVolumes: what the
// operator's pct set gives it.
type fakeDevices struct {
	*fakeVolumes
	devs map[int][]proxmoxapi.Device
}

func (f *fakeDevices) Devices(_ context.Context, _ string, vmid int) ([]proxmoxapi.Device, error) {
	return slices.Clone(f.devs[vmid]), nil
}

// vpnManifest is a VPN client that needs /dev/net/tun, and an
// application that needs nothing of the node.
func vpnManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion:   "urbex/v1",
		Project:      "media",
		Environments: []string{"prod"},
		Services: []manifest.Service{
			{Name: "vault", Image: "vaultwarden/server", Version: "1.34.3", Port: 80},
			{Name: "gluetun", Image: "qmcgaw/gluetun", Version: "3.40.0", Port: 8000, Devices: []string{"/dev/net/tun"}},
		},
	}
}

func devicesOrchestrator(t *testing.T) (*Orchestrator, *fakeDevices, *fakeRollout, *strings.Builder) {
	t.Helper()
	o, ro, _, _, _ := imagesOrchestrator(vpnManifest())
	out := &strings.Builder{}
	o.Out = out
	devs := &fakeDevices{fakeVolumes: newFakeVolumes(), devs: map[int][]proxmoxapi.Device{}}
	o.Volumes = devs
	return o, devs, ro, out
}

func TestApply_SkipsApplicationsUntilTheirDevicesAreBound(t *testing.T) {
	dir := t.TempDir()
	o, devs, ro, out := devicesOrchestrator(t)
	ctx := context.Background()

	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "gluetun") {
		t.Fatalf("expected apply to fail naming gluetun, got %v\n%s", err, out)
	}
	vmid := vmidOf(t, o, dir, "gluetun")
	command := fmt.Sprintf("pct set %d -dev0 /dev/net/tun", vmid)
	for _, want := range []string{command, "root@pam", "machine: vm"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in the output:\n%s", want, out)
		}
	}
	if !slices.Equal(ro.deployed, []string{"vault"}) || o.repoHas(dir, "gluetun") {
		t.Errorf("only vault should be deployed, got %v", ro.deployed)
	}

	// The operator runs the command on the running LXC: Proxmox applies
	// it on the next restart, which the next apply does, then deploys.
	devs.devs[vmid] = []proxmoxapi.Device{{Key: "dev0", Path: "/dev/net/tun"}}
	devs.pending[vmid] = []string{"dev0"}
	out.Reset()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply after the device: %v\n%s", err, out)
	}
	if !slices.Contains(ro.deployed, "gluetun") || !o.repoHas(dir, "gluetun") {
		t.Errorf("gluetun should be deployed now, got %v", ro.deployed)
	}
	if !slices.Equal(devs.reboots, []int{vmid}) {
		t.Errorf("a device pending on a running LXC needs a restart, got %v", devs.reboots)
	}
}

// A device already passed under another key, or with options, is bound;
// a second one takes the next free key.
func TestApply_ADeviceTakesTheNextFreeKey(t *testing.T) {
	dir := t.TempDir()
	o, devs, _, out := devicesOrchestrator(t)
	o.Manifest.Services[1].Devices = []string{"/dev/net/tun", "/dev/dri/renderD128"}
	ctx := context.Background()
	_ = o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	vmid := vmidOf(t, o, dir, "gluetun")
	devs.devs[vmid] = []proxmoxapi.Device{{Key: "dev0", Path: "/dev/fuse"}, {Key: "dev1", Path: "/dev/net/tun"}}
	out.Reset()
	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected apply to wait for the second device")
	}
	if want := fmt.Sprintf("pct set %d -dev2 /dev/dri/renderD128", vmid); !strings.Contains(out.String(), want) {
		t.Errorf("expected %q:\n%s", want, out)
	}
	if strings.Contains(out.String(), "/dev/net/tun\n") {
		t.Errorf("/dev/net/tun is bound already:\n%s", out)
	}
}

// Without the Proxmox API urbex can't tell whether a device is bound.
func TestApply_DevicesNeedTheProxmoxAPI(t *testing.T) {
	o, _, _, _ := devicesOrchestrator(t)
	o.Volumes = newFakeVolumes()
	err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "devices need the Proxmox API") {
		t.Errorf("expected devices refused without the API, got %v", err)
	}
}
