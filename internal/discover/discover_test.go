package discover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// fakeProxmox answers as the Cubotto lab's node does to a pool token.
type fakeProxmox struct {
	versionErr error
	members    []proxmoxapi.Member
	taken      map[int]bool
	templates  []string
}

func (f *fakeProxmox) Version(context.Context) (string, error) { return "9.2.21", f.versionErr }
func (f *fakeProxmox) Nodes(context.Context) ([]string, error) { return []string{"pve"}, nil }
func (f *fakeProxmox) Permissions(context.Context) (map[string]map[string]bool, error) {
	return map[string]map[string]bool{
		"/pool/urbex":                   {"VM.Allocate": true},
		"/storage/local-zfs":            {"Datastore.AllocateSpace": true},
		"/sdn/zones/localnetwork/vmbr0": {"SDN.Use": true},
	}, nil
}
func (f *fakeProxmox) Storages(context.Context, string) ([]proxmoxapi.Storage, error) {
	return []proxmoxapi.Storage{
		{Name: "big", Type: "dir", Content: []string{"images", "rootdir"}, AvailGB: 4000, Active: true},
		{Name: "local", Type: "dir", Content: []string{"import", "vztmpl"}, Active: true},
		{Name: "local-zfs", Type: "zfspool", Content: []string{"images", "rootdir"}, AvailGB: 881, TotalGB: 896, Active: true},
	}, nil
}
func (f *fakeProxmox) Volumes(_ context.Context, _, storage, content string) ([]string, error) {
	if storage == "local" && content == "vztmpl" {
		return f.templates, nil
	}
	return nil, nil
}
func (f *fakeProxmox) Bridges(context.Context, string) ([]proxmoxapi.Bridge, error) {
	return []proxmoxapi.Bridge{{Name: "vmbr0", CIDR: "192.168.1.33/24", Gateway: "192.168.1.1"}}, nil
}
func (f *fakeProxmox) VMIDFree(_ context.Context, id int) (bool, error) { return !f.taken[id], nil }
func (f *fakeProxmox) PoolMembers(context.Context, string) ([]proxmoxapi.Member, error) {
	return f.members, nil
}

type fakeCloudflare struct{ zones []cloudflare.Zone }

func (f *fakeCloudflare) VerifyAccountToken(_ context.Context, account string) (string, error) {
	if account != "acc" {
		return "", errors.New("not this account's token")
	}
	return "active", nil
}
func (f *fakeCloudflare) Zones(context.Context) ([]cloudflare.Zone, error) { return f.zones, nil }

func zone(id, name, account string) cloudflare.Zone {
	z := cloudflare.Zone{ID: id, Name: name}
	z.Account.ID = account
	return z
}

func labTemplates() []string {
	return []string{
		"local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst",
		"local:vztmpl/debian-13-standard_13.10-1_arm64.tar.zst",
		"local:vztmpl/debian-13-standard_13.2-1_amd64.tar.zst",
		"local:vztmpl/debian-13-standard_13.10-1_amd64.tar.zst",
	}
}

// What the lab gives: the values are chosen, the block skips what
// answers, and the proposal is a valid platform config.
func TestRun_Lab(t *testing.T) {
	answering := map[string]bool{"192.168.1.200": true, "192.168.1.205": true, "192.168.1.150": true}
	in := Input{
		APIURL:       "192.168.1.33",
		Insecure:     true,
		TokenID:      "teg@pve!terraform",
		Proxmox:      &fakeProxmox{templates: labTemplates()},
		Cloudflare:   &fakeCloudflare{zones: []cloudflare.Zone{zone("z1", "example.com", "acc")}},
		Probe:        func(_ context.Context, ip string) bool { return answering[ip] },
		SSHPublicKey: "ssh-ed25519 AAAA urbex\n",
	}
	r := Run(context.Background(), in)
	if len(r.Errors) > 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	p, n := r.Proxmox, r.Network
	if p.APIURL != "https://192.168.1.33:8006/api2/json" || p.Node != "pve" || p.Pool != "urbex" || p.Bridge != "vmbr0" {
		t.Errorf("proxmox = %+v", p)
	}
	if p.StoragePool != "local-zfs" {
		t.Errorf("storage = %s, want the zfs pool before a bigger dir", p.StoragePool)
	}
	if p.LXCTemplate != "local:vztmpl/debian-13-standard_13.10-1_amd64.tar.zst" {
		t.Errorf("template = %s, want the newest amd64 Debian 13", p.LXCTemplate)
	}
	if n.CIDR != "192.168.1.0/24" || n.Gateway != "192.168.1.1" || !n.Scanned {
		t.Errorf("network = %+v", n)
	}
	// .200 and .205 answer: the nearest free 20 to .200 are .206-.225.
	if n.BaseHostOffset != 206 {
		t.Errorf("baseHostOffset = %d, want 206 (in use: %v)", n.BaseHostOffset, n.InUse)
	}
	if r.Cloudflare == nil || r.Cloudflare.Domain != "example.com" || r.Cloudflare.AccountID != "acc" || !r.Cloudflare.TokenActive {
		t.Errorf("cloudflare = %+v", r.Cloudflare)
	}

	path := filepath.Join(t.TempDir(), "urbex.platform.yaml")
	os.WriteFile(path, []byte(r.Proposal), 0o644)
	cfg, err := platform.LoadFile(path)
	if err != nil {
		t.Fatalf("the proposal doesn't parse: %v\n%s", err, r.Proposal)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Errorf("the proposal isn't valid: %v\n%s", errs, r.Proposal)
	}
	if cfg.Proxmox.Network.BaseHostOffset != 206 || cfg.Proxmox.Pool != "urbex" || !cfg.Proxmox.Insecure || cfg.Cloudflare.ZoneID != "z1" || cfg.Proxmox.SSHPublicKey != "ssh-ed25519 AAAA urbex" {
		t.Errorf("config = %+v", cfg.Proxmox)
	}
	if !strings.Contains(r.Proposal, "FILL IN: who may open Gitea and Komodo") {
		t.Errorf("the Access emails should be marked to fill in:\n%s", r.Proposal)
	}
}

// Without Cloudflare, a scan or a key, the proposal still parses, says
// what to check, and stays on the LAN.
func TestRun_Minimal(t *testing.T) {
	r := Run(context.Background(), Input{APIURL: "https://pve.lan:8006", Proxmox: &fakeProxmox{templates: labTemplates()}})
	if r.Network.Scanned || r.Network.BaseHostOffset != 200 {
		t.Errorf("network = %+v, want .200 unscanned", r.Network)
	}
	for _, want := range []string{"wasn't scanned", "no SSH public key"} {
		if !strings.Contains(strings.Join(r.Warnings, "|"), want) {
			t.Errorf("warnings %v lack %q", r.Warnings, want)
		}
	}
	for _, want := range []string{"domain: home.arpa", "# cloudflare:", "#   quickTunnel: true", "not scanned"} {
		if !strings.Contains(r.Proposal, want) {
			t.Errorf("proposal lacks %q:\n%s", want, r.Proposal)
		}
	}
}

// An existing platform, a taken VMID, no template, a refused token.
func TestRun_Problems(t *testing.T) {
	r := Run(context.Background(), Input{APIURL: "pve", Proxmox: &fakeProxmox{
		templates: nil, taken: map[int]bool{9000: true},
		members: []proxmoxapi.Member{{VMID: 9000, Name: "urbex-gitea", Type: "lxc"}},
	}})
	if !r.Proxmox.Platform || !strings.Contains(strings.Join(r.Warnings, "|"), "an Urbex platform exists in pool urbex") {
		t.Errorf("existing platform: %+v %v", r.Proxmox, r.Warnings)
	}
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "pveam") || r.Proposal != "" {
		t.Errorf("no template: errors %v, proposal %q", r.Errors, r.Proposal)
	}

	r = Run(context.Background(), Input{APIURL: "pve", Proxmox: &fakeProxmox{versionErr: errors.New("proxmox api GET /version returned 401 Unauthorized")}})
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "refuses the token") {
		t.Errorf("refused token: %v", r.Errors)
	}
}

func TestNormalizeAPIURL(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.33":                           "https://192.168.1.33:8006/api2/json",
		"https://pve.lan:8006":                   "https://pve.lan:8006/api2/json",
		"https://pve.lan:8006/":                  "https://pve.lan:8006/api2/json",
		"https://pve.lan:8006/api2/json":         "https://pve.lan:8006/api2/json",
		"https://pve.example.com:443/api2/json/": "https://pve.example.com:443/api2/json",
	} {
		if got := NormalizeAPIURL(in); got != want {
			t.Errorf("NormalizeAPIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The echo request's checksum makes the whole message sum to 0xffff.
func TestEchoRequest(t *testing.T) {
	b := echoRequest(0x1234, 7)
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	if sum != 0xffff || b[0] != 8 || b[4] != 0x12 || b[5] != 0x34 || b[7] != 7 {
		t.Errorf("echo request %x, sum %x", b, sum)
	}
}

func TestNaturalLess(t *testing.T) {
	if !naturalLess("debian-13-standard_13.2-1", "debian-13-standard_13.10-1") || naturalLess("192.168.1.10", "192.168.1.9") {
		t.Error("numbers must compare as numbers")
	}
}
