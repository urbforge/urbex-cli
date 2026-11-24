package provision

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
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
	if err == nil || !strings.Contains(err.Error(), "the devices they need: gluetun") {
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

	// The operator runs the command on the running LXC. Proxmox VE 9
	// passes it at once (as on the lab); one that leaves it pending gets
	// it on the restart the next apply does, then the app deploys.
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

// A device Proxmox passed to the running LXC at once, as Proxmox VE 9
// does, needs no restart.
func TestApply_NoRestartForADevicePassedAtOnce(t *testing.T) {
	dir := t.TempDir()
	o, devs, ro, out := devicesOrchestrator(t)
	ctx := context.Background()
	_ = o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	vmid := vmidOf(t, o, dir, "gluetun")
	devs.devs[vmid] = []proxmoxapi.Device{{Key: "dev0", Path: "/dev/net/tun"}}
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out)
	}
	if len(devs.reboots) != 0 || !slices.Contains(ro.deployed, "gluetun") {
		t.Errorf("reboots %v, deployed %v", devs.reboots, ro.deployed)
	}
}

// The applications of a group share their LXC: a device two of them
// need is one command, and different devices get different keys.
func TestApply_DevicesOfAGroupShareTheLXCsKeys(t *testing.T) {
	dir := t.TempDir()
	o, _, _, out := devicesOrchestrator(t)
	o.Manifest.Services = []manifest.Service{
		{Name: "gluetun", Image: "qmcgaw/gluetun", Version: "3", Port: 8000, LXC: "dl", Devices: []string{"/dev/net/tun"}},
		{Name: "qbittorrent", Image: "linuxserver/qbittorrent", Version: "5", Port: 8080, LXC: "dl", Devices: []string{"/dev/net/tun", "/dev/fuse"}},
	}
	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err == nil {
		t.Fatal("expected apply to wait for the devices")
	}
	vmid := vmidOf(t, o, dir, "dl") // the group's machine, media-dl-prod
	tun := fmt.Sprintf("pct set %d -dev0 /dev/net/tun", vmid)
	fuse := fmt.Sprintf("pct set %d -dev1 /dev/fuse", vmid)
	if strings.Count(out.String(), tun) != 1 || strings.Count(out.String(), fuse) != 1 {
		t.Errorf("expected %q once and %q once:\n%s", tun, fuse, out)
	}
}

// A group's LXC with a share and a device: the share's mount point and
// the device are planned apart, mp<n> and dev<n>, each from 0 - one key
// never takes the other's number.
func TestApply_AGroupsShareAndDeviceKeysApart(t *testing.T) {
	dir := t.TempDir()
	o, _, _, out := devicesOrchestrator(t)
	o.Config.Storage.Shares = map[string]platform.Share{"downloads": {Path: "/mnt/pve/nas/downloads"}}
	o.Manifest.Services = []manifest.Service{
		{Name: "gluetun", Image: "qmcgaw/gluetun", Version: "3", Port: 8000, LXC: "dl", Devices: []string{"/dev/net/tun"}},
		{Name: "qbittorrent", Image: "linuxserver/qbittorrent", Version: "5", Port: 8080, LXC: "dl", Volumes: []manifest.Volume{
			{Share: "downloads", Path: "/downloads"},
		}, Devices: []string{"/dev/fuse"}},
	}
	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err == nil {
		t.Fatal("expected apply to wait for the share and the devices")
	}
	vmid := vmidOf(t, o, dir, "dl")
	for _, want := range []string{
		fmt.Sprintf("pct set %d -dev0 /dev/net/tun", vmid),
		fmt.Sprintf("pct set %d -mp0 /mnt/pve/nas/downloads,mp=/srv/shares/downloads", vmid),
		fmt.Sprintf("pct set %d -dev1 /dev/fuse", vmid),
	} {
		if strings.Count(out.String(), want) != 1 {
			t.Errorf("expected %q once:\n%s", want, out)
		}
	}
}
