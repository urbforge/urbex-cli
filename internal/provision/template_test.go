package provision

import (
	"bytes"
	"context"
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
