package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// templateProxmox is a fake Proxmox that can build templates: cts holds
// the LXCs of the template range by VMID; inUse are templates with
// clones, which Proxmox won't delete.
type templateProxmox struct {
	fakeProxmox
	cts     map[int]*proxmoxapi.ContainerInfo
	inUse   map[int]bool
	calls   []string
	failing string // the call that fails, if any
}

func (p *templateProxmox) step(name string, vmid int) error {
	p.calls = append(p.calls, fmt.Sprintf("%s %d", name, vmid))
	if name == p.failing {
		return errors.New("403 Forbidden")
	}
	return nil
}
func (p *templateProxmox) Templates(ctx context.Context, node string, from, to int) ([]int, error) {
	var ids []int
	for id := from; id <= to; id++ {
		if c := p.cts[id]; c != nil && c.Template {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (p *templateProxmox) Container(ctx context.Context, node string, vmid int) (*proxmoxapi.ContainerInfo, error) {
	return p.cts[vmid], nil
}
func (p *templateProxmox) CreateContainer(ctx context.Context, node string, spec proxmoxapi.ContainerSpec) error {
	if spec.Hostname != TemplateHostname || spec.Pool != "urbex" || !strings.HasPrefix(spec.IPCIDR, "192.168.1.209/") {
		return errors.New("unexpected spec")
	}
	if err := p.step("create", spec.VMID); err != nil {
		return err
	}
	if p.cts == nil {
		p.cts = map[int]*proxmoxapi.ContainerInfo{}
	}
	p.cts[spec.VMID] = &proxmoxapi.ContainerInfo{}
	return nil
}
func (p *templateProxmox) StartContainer(ctx context.Context, node string, vmid int) error {
	return p.step("start", vmid)
}
func (p *templateProxmox) StopContainer(ctx context.Context, node string, vmid int) error {
	return p.step("stop", vmid)
}
func (p *templateProxmox) ConvertToTemplate(ctx context.Context, node string, vmid int) error {
	p.cts[vmid].Template = true
	return p.step("template", vmid)
}
func (p *templateProxmox) SetDescription(ctx context.Context, node string, vmid int, d string) error {
	p.cts[vmid].Description = d
	return p.step("describe", vmid)
}
func (p *templateProxmox) DeleteContainer(ctx context.Context, node string, vmid int) error {
	if p.inUse[vmid] {
		return errors.New("can't remove template with linked clones")
	}
	delete(p.cts, vmid)
	return p.step("delete", vmid)
}

func cloneVMIDOf(t *testing.T, dir string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, "terraform", "base", "terraform.auto.tfvars.json"))
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, `"clone_vm_id"`) {
			return strings.Trim(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), ",")
		}
	}
	return ""
}

// The platform phase builds the template when it's missing, before the
// LXCs, which are then cloned from it (redemptor#18).
func TestRun_BuildsTheTemplateAndClonesFromIt(t *testing.T) {
	dir := t.TempDir()
	p := &templateProxmox{}
	o, runner, _ := newRunOrchestrator(t, &p.fakeProxmox)
	o.Proxmox = p
	o.Config.Proxmox.Pool = "urbex"
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(p.calls, ","); got != "create 9090,start 9090,stop 9090,template 9090,describe 9090" {
		t.Errorf("proxmox calls = %s", got)
	}
	if first := runner.calls[0]; first.Name != "ansible-playbook" || first.Args[len(first.Args)-1] != "template-playbook.yml" {
		t.Errorf("the template must be prepared first, got %+v", first)
	}
	if v := cloneVMIDOf(t, dir); v != "9090" {
		t.Errorf("clone_vm_id = %q, want 9090", v)
	}
	if !strings.Contains(p.cts[9090].Description, "version "+TemplateVersion(o.Config)) {
		t.Errorf("description %q lacks the version", p.cts[9090].Description)
	}
	inv, _ := os.ReadFile(filepath.Join(dir, "ansible", templateInventory))
	if !strings.Contains(string(inv), "192.168.1.209") || !strings.Contains(string(inv), "alloy_image: docker.io/grafana/alloy:") {
		t.Errorf("template inventory:\n%s", inv)
	}
}

// A current template is reused. A new version is built at another VMID:
// the older template, still the source of linked clones, can't go yet;
// one no LXC uses any more is deleted.
func TestRun_ReusesACurrentTemplateBuildsANewVersionAlongside(t *testing.T) {
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	o.Config.Proxmox.Pool = "urbex"
	current := &templateProxmox{cts: map[int]*proxmoxapi.ContainerInfo{
		9090: {Template: true, Description: templateDescription(TemplateVersion(o.Config))}}}
	o.Proxmox = current
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(current.calls) != 0 {
		t.Errorf("a current template is reused, got calls %v", current.calls)
	}

	dir := t.TempDir()
	o2, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	o2.Config.Proxmox.Pool = "urbex"
	old := &templateProxmox{
		cts: map[int]*proxmoxapi.ContainerInfo{
			9090: {Template: true, Description: templateDescription("000000000000")},
			9091: {Template: true, Description: templateDescription("111111111111")},
			9092: {}, // an interrupted build
		},
		inUse: map[int]bool{9090: true},
	}
	o2.Proxmox = old
	if err := o2.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "stop 9092,delete 9092,create 9092,start 9092,stop 9092,template 9092,describe 9092,delete 9091"
	if got := strings.Join(old.calls, ","); got != want {
		t.Errorf("calls = %s\nwant    %s", got, want)
	}
	if old.cts[9090] == nil || old.cts[9091] != nil {
		t.Error("the template with clones stays, the unused one goes")
	}
	if v := cloneVMIDOf(t, dir); v != "9092" {
		t.Errorf("clone_vm_id = %q, want the new template 9092", v)
	}
}

// The template only saves time: if Proxmox refuses, LXCs come from the
// Debian template, with a warning, and bootstrap goes on.
func TestRun_WithoutTheTemplateFallsBackToDebian(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	p := &templateProxmox{failing: "create"}
	o.Proxmox = p
	o.Config.Proxmox.Pool = "urbex"
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v := cloneVMIDOf(t, dir); v != "0" {
		t.Errorf("clone_vm_id = %q, want 0", v)
	}
	if !strings.Contains(o.Out.(interface{ String() string }).String(), "couldn't prepare the LXC template") {
		t.Error("expected a warning")
	}
}

// The template's version follows what it is built from.
func TestTemplateVersion(t *testing.T) {
	cfg := validConfig()
	v := TemplateVersion(cfg)
	if len(v) != 12 || v != TemplateVersion(cfg) {
		t.Errorf("version %q: 12 stable hex characters expected", v)
	}
	cfg.Proxmox.LXCTemplate = "local:vztmpl/debian-14.tar.zst"
	if TemplateVersion(cfg) == v {
		t.Error("a new Debian template must change the version")
	}
}
