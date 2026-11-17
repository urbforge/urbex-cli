package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// templateLister is a Proxmox with a current urbex template at VMID 9090.
type templateLister struct {
	fakeLister
	version string
}

func (p templateLister) Templates(context.Context, string, int, int) ([]int, error) {
	return []int{9090}, nil
}
func (p templateLister) Container(context.Context, string, int) (*proxmoxapi.ContainerInfo, error) {
	return &proxmoxapi.ContainerInfo{Template: true, Description: "version " + p.version}, nil
}
func (templateLister) CreateContainer(context.Context, string, proxmoxapi.ContainerSpec) error {
	return nil
}
func (templateLister) StartContainer(context.Context, string, int) error         { return nil }
func (templateLister) StopContainer(context.Context, string, int) error          { return nil }
func (templateLister) ConvertToTemplate(context.Context, string, int) error      { return nil }
func (templateLister) SetDescription(context.Context, string, int, string) error { return nil }
func (templateLister) DeleteContainer(context.Context, string, int) error        { return nil }

// An environment's LXCs cloned from urbex's template get its disk; a
// second apply grows them to the manifest's size.
func TestApply_GrowsTheDisksOfClonedLXCs(t *testing.T) {
	r := &fakeRunner{}
	cfg := validPlatformConfig()
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      cfg,
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
		Proxmox:     templateLister{version: bootstrap.TemplateVersion(cfg)},
	}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	applies := 0
	for _, c := range r.calls {
		if c.Name == "terraform" && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("terraform apply ran %d times, want 2: %+v", applies, r.calls)
	}
}

// With only an outdated urbex template, apply still clones from it and
// says the template is out of date; with none, it says the LXCs come
// from the Debian template (urbforge/urbex-cli#108).
func TestApply_ClonesFromAnOutdatedTemplateAndSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		clone         int
		says          string
	}{
		{"outdated", "0123456789ab", 9090, "urbex's LXC template (9090) is out of date"},
		{"current", "", 9090, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validPlatformConfig()
			version := tc.version
			if version == "" {
				version = bootstrap.TemplateVersion(cfg)
			}
			var out bytes.Buffer
			repo := t.TempDir()
			o := &Orchestrator{
				Manifest: validManifest(), Config: cfg, Credentials: validCredentials(),
				Runner: &fakeRunner{}, Out: &out, Rollout: &fakeRollout{},
				Proxmox: templateLister{version: version},
			}
			if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: repo}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(repo, "terraform", "projects", o.Manifest.Project, "staging", "terraform.auto.tfvars.json"))
			if err != nil {
				t.Fatal(err)
			}
			var vars struct {
				CloneVMID int `json:"clone_vm_id"`
			}
			if err := json.Unmarshal(data, &vars); err != nil || vars.CloneVMID != tc.clone {
				t.Errorf("clone_vm_id = %d (%v), want %d", vars.CloneVMID, err, tc.clone)
			}
			if got := strings.Contains(out.String(), "is out of date"); got != (tc.says != "") || !strings.Contains(out.String(), tc.says) {
				t.Errorf("output %q, want it to say %q", out.String(), tc.says)
			}
		})
	}
}
