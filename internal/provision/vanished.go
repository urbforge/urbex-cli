package provision

import (
	"context"

	"github.com/urbforge/urbex-cli/internal/tfstate"
)

// ContainerLister is the subset of proxmoxapi.Client the orchestrator uses
// to tell which LXCs still exist.
type ContainerLister = tfstate.ContainerLister

// forgetVanished drops from the Terraform state in tfDir the LXCs that no
// longer exist on Proxmox (see tfstate.ForgetVanished).
func (o *Orchestrator) forgetVanished(ctx context.Context, tfDir string, env []string) error {
	return tfstate.ForgetVanished(ctx, o.Proxmox, o.Config.Proxmox.Node, o.Runner, tfDir, env, o.Out)
}
