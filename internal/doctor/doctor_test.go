package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

func fakeSystem(have map[string]bool, env map[string]string) System {
	return System{
		LookPath: func(name string) (string, error) {
			if have[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Output: func(ctx context.Context, name string, args ...string) (string, error) {
			return name + " 1.2.3\nmore\n", nil
		},
		Exists: func(path string) bool { return have[path] },
		Getenv: func(k string) string { return env[k] },
		Home:   "/home/me",
	}
}

func find(t *testing.T, r *Report, group, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Group == group && c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %s/%s in %+v", group, name, r.Checks)
	return Check{}
}

// A missing tool fails with how to get it; a present one shows its
// version's first line.
func TestTools(t *testing.T) {
	r := &Report{}
	Tools(context.Background(), fakeSystem(map[string]bool{"git": true, "ssh": true, "terraform": true, "ansible-playbook": true, "sops": true}, nil), r)
	if c := find(t, r, "tools", "git"); c.Status != OK || c.Detail != "git 1.2.3" {
		t.Errorf("git = %+v", c)
	}
	if c := find(t, r, "tools", "age"); c.Status != Fail || !strings.Contains(c.Fix, "urbex-op") {
		t.Errorf("age = %+v", c)
	}
	if !r.Failed() || !strings.Contains(r.Summary(), "1 failed") {
		t.Errorf("summary = %s", r.Summary())
	}
}

func TestWorkspaceChecks(t *testing.T) {
	sys := fakeSystem(map[string]bool{"/home/me/.urbex/credentials.yaml": true, "/home/me/.ssh/urbex": true, "/work/gitops/urbex.platform.yaml": true}, nil)
	r := &Report{}
	WorkspaceChecks(sys, Workspace{CredentialsFile: "/home/me/.urbex/credentials.yaml", CredentialsShown: "/Users/me/ws/home/.urbex/credentials.yaml", GitOpsRepo: "/work/gitops"}, r)
	if c := find(t, r, "workspace", "credentials file"); c.Detail != "/Users/me/ws/home/.urbex/credentials.yaml" {
		t.Errorf("the file is shown where the operator sees it: %+v", c)
	}
	for _, name := range []string{"credentials file", "credentials", "SSH key", "GitOps repo", "platform config"} {
		if c := find(t, r, "workspace", name); c.Status != OK {
			t.Errorf("%s = %+v, want ok", name, c)
		}
	}

	// Nothing yet: what's missing fails or warns, each with its fix.
	r = &Report{}
	WorkspaceChecks(fakeSystem(nil, nil), Workspace{CredentialsFile: "/nope", Missing: []string{"URBEX_PROXMOX_TOKEN"}, GitOpsRepoErr: errors.New("no GitOps repo")}, r)
	for name, want := range map[string]string{"credentials file": Warn, "credentials": Fail, "SSH key": Warn, "GitOps repo": Fail} {
		if c := find(t, r, "workspace", name); c.Status != want || c.Fix == "" {
			t.Errorf("%s = %+v, want %s with a fix", name, c, want)
		}
	}

	// An invalid platform config fails with its errors.
	r = &Report{}
	WorkspaceChecks(sys, Workspace{CredentialsFile: "/home/me/.urbex/credentials.yaml", GitOpsRepo: "/work/gitops", ConfigErrs: []string{"proxmox.node is required"}}, r)
	if c := find(t, r, "workspace", "platform config"); c.Status != Fail || !strings.Contains(c.Detail, "proxmox.node") {
		t.Errorf("platform config = %+v", c)
	}
}

type fakeProxmox struct {
	versionErr error
	perms      map[string]map[string]bool
	vztmpl     []string
	bridges    []string
}

func (f *fakeProxmox) Version(context.Context) (string, error) { return "9.2.21", f.versionErr }
func (f *fakeProxmox) Nodes(context.Context) ([]string, error) { return []string{"pve"}, nil }
func (f *fakeProxmox) Pools(context.Context) ([]string, error) { return []string{"urbex"}, nil }
func (f *fakeProxmox) Permissions(context.Context) (map[string]map[string]bool, error) {
	return f.perms, nil
}
func (f *fakeProxmox) Storages(context.Context, string) ([]proxmoxapi.Storage, error) {
	return []proxmoxapi.Storage{
		{Name: "local", Type: "dir", Content: []string{"backup", "import", "iso", "vztmpl"}},
		{Name: "local-zfs", Type: "zfspool", Content: []string{"images", "rootdir"}, AvailGB: 900, TotalGB: 950},
	}, nil
}
func (f *fakeProxmox) Volumes(_ context.Context, _, storage, content string) ([]string, error) {
	if storage == "local" && content == "vztmpl" {
		return f.vztmpl, nil
	}
	return nil, nil
}
func (f *fakeProxmox) Bridges(context.Context, string) ([]proxmoxapi.Bridge, error) {
	var out []proxmoxapi.Bridge
	for _, b := range f.bridges {
		out = append(out, proxmoxapi.Bridge{Name: b})
	}
	return out, nil
}

func labConfig() *platform.Config {
	cfg := &platform.Config{Domain: "example.com"}
	cfg.Proxmox = platform.ProxmoxConfig{APIURL: "https://pve:8006/api2/json", Node: "pve", StoragePool: "local-zfs", Pool: "urbex",
		LXCTemplate: "local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst", Network: platform.NetworkConfig{Bridge: "vmbr0"}}
	return cfg
}

func poolToken() map[string]map[string]bool {
	all := map[string]bool{}
	for _, p := range poolPrivileges {
		all[p] = true
	}
	return map[string]map[string]bool{
		"/pool/urbex":                   all,
		"/storage/local-zfs":            {"Datastore.AllocateSpace": true, "Datastore.Audit": true},
		"/sdn/zones/localnetwork/vmbr0": {"SDN.Use": true},
	}
}

// The lab's case: a pool-scoped token with what urbex needs.
func TestProxmoxChecks_PoolToken(t *testing.T) {
	p := &fakeProxmox{perms: poolToken(), vztmpl: []string{"local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst"}, bridges: []string{"vmbr0"}}
	r := &Report{}
	ProxmoxChecks(context.Background(), p, labConfig(), r)
	for _, c := range r.Checks {
		if c.Status != OK {
			t.Errorf("%s = %+v, want ok", c.Name, c)
		}
	}
	if c := find(t, r, "proxmox", "API"); !strings.Contains(c.Detail, "9.2.21") {
		t.Errorf("API = %+v", c)
	}
}

// Each problem is its own failure, with what to do.
func TestProxmoxChecks_Problems(t *testing.T) {
	perms := poolToken()
	delete(perms, "/sdn/zones/localnetwork/vmbr0")
	p := &fakeProxmox{perms: perms, vztmpl: []string{"local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst"}, bridges: []string{"vmbr1"}}
	r := &Report{}
	ProxmoxChecks(context.Background(), p, labConfig(), r)
	if c := find(t, r, "proxmox", "privileges"); c.Status != Fail || !strings.Contains(c.Detail, "SDN.Use on /sdn/zones/localnetwork/vmbr0") {
		t.Errorf("privileges = %+v", c)
	}
	if c := find(t, r, "proxmox", "LXC template"); c.Status != Fail || !strings.Contains(c.Detail, "debian-12") || !strings.Contains(c.Fix, "pveam") {
		t.Errorf("LXC template = %+v", c)
	}
	if c := find(t, r, "proxmox", "bridge"); c.Status != Fail {
		t.Errorf("bridge = %+v", c)
	}
}

// A refused token says so, and the checks that need it don't run.
func TestProxmoxChecks_TokenRefused(t *testing.T) {
	r := &Report{}
	ProxmoxChecks(context.Background(), &fakeProxmox{versionErr: errors.New("proxmox api GET /version returned 401 Unauthorized")}, labConfig(), r)
	if len(r.Checks) != 1 || r.Checks[0].Status != Fail || !strings.Contains(r.Checks[0].Fix, "token is refused") {
		t.Errorf("checks = %+v", r.Checks)
	}
	r = &Report{}
	ProxmoxChecks(context.Background(), nil, labConfig(), r)
	if len(r.Checks) != 1 || r.Checks[0].Status != Skip {
		t.Errorf("without a token = %+v", r.Checks)
	}
}

// root@pam has its privileges on /, which reach everything.
func TestHas_Propagates(t *testing.T) {
	perms := map[string]map[string]bool{"/": {"SDN.Use": true}, "/pool/urbex": {"VM.Allocate": true}}
	if !has(perms, "/sdn/zones/localnetwork/vmbr0", "SDN.Use") || !has(perms, "/pool/urbex", "VM.Allocate") || has(perms, "/storage/local", "VM.Allocate") {
		t.Error("privileges should propagate from a parent path, and only from there")
	}
}

type fakeCloudflare struct {
	status string
	zones  []cloudflare.Zone
}

func (f *fakeCloudflare) VerifyToken(context.Context) (string, error)      { return f.status, nil }
func (f *fakeCloudflare) Zones(context.Context) ([]cloudflare.Zone, error) { return f.zones, nil }

func TestCloudflareChecks(t *testing.T) {
	cfg := labConfig()
	r := &Report{}
	CloudflareChecks(context.Background(), nil, cfg, r)
	if c := find(t, r, "cloudflare", "account"); c.Status != Skip {
		t.Errorf("no Cloudflare = %+v", c)
	}

	cfg.Cloudflare = platform.CloudflareConfig{AccountID: "a", ZoneID: "z1"}
	r = &Report{}
	CloudflareChecks(context.Background(), nil, cfg, r)
	if c := find(t, r, "cloudflare", "token"); c.Status != Fail {
		t.Errorf("no token = %+v", c)
	}

	r = &Report{}
	CloudflareChecks(context.Background(), &fakeCloudflare{status: "active", zones: []cloudflare.Zone{{ID: "z1", Name: "example.com"}}}, cfg, r)
	if c := find(t, r, "cloudflare", "zone"); c.Status != OK || c.Detail != "example.com" {
		t.Errorf("zone = %+v", c)
	}

	r = &Report{}
	CloudflareChecks(context.Background(), &fakeCloudflare{status: "active", zones: []cloudflare.Zone{{ID: "z2", Name: "other.com"}}}, cfg, r)
	if c := find(t, r, "cloudflare", "zone"); c.Status != Fail {
		t.Errorf("wrong zone = %+v", c)
	}
}
