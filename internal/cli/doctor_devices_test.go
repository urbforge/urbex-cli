package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
)

// The applications of a group share their LXC: doctor checks it once,
// with the devices of all of them.
func TestDeviceMachines_OnePerMachine(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "urbex.yaml")
	body := `apiVersion: urbex/v1
project: media
environments: [prod]
services:
  - name: gluetun
    image: qmcgaw/gluetun
    version: "3"
    port: 8000
    lxc: dl
    devices: [/dev/net/tun]
  - name: qbittorrent
    image: linuxserver/qbittorrent
    version: "5"
    port: 8080
    lxc: dl
    devices: [/dev/net/tun, /dev/fuse]
  - name: jellyfin
    image: jellyfin/jellyfin
    version: "10"
    port: 8096
    devices: [/dev/dri/renderD128]
`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := `{"entries": {"media-dl-prod": {"vmid": 9120, "ip": "10.0.0.20"}}, "nextVmid": 9121}`
	if err := os.WriteFile(filepath.Join(dir, "state", "allocations.json"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	machines, err := deviceMachines(manifest, dir, &platform.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 2 {
		t.Fatalf("machines = %+v", machines)
	}
	dl, jf := machines[0], machines[1]
	if dl.Service != "dl" || dl.VMID != 9120 || !slices.Equal(dl.Devices, []string{"/dev/net/tun", "/dev/fuse"}) {
		t.Errorf("the group's machine: %+v", dl)
	}
	if jf.Service != "jellyfin" || jf.VMID != 0 || !slices.Equal(jf.Devices, []string{"/dev/dri/renderD128"}) {
		t.Errorf("jellyfin's own machine, not created yet: %+v", jf)
	}
}
