package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/urbforge/urbex-cli/internal/runner"
)

// ContainerLister is the subset of proxmoxapi.Client the orchestrator uses
// to tell which LXCs still exist.
type ContainerLister interface {
	ListContainerIDs(ctx context.Context, node string) ([]int, error)
}

// forgetVanished drops from a Terraform state the LXCs that no longer exist
// on Proxmox. Terraform would notice by itself with an unrestricted token,
// but a token scoped to a resource pool gets "403 Permission check failed"
// for a VMID that isn't in the pool - which is what a deleted LXC is - so
// every plan, apply, and destroy would fail on it.
func (o *Orchestrator) forgetVanished(ctx context.Context, tfDir string, env []string) error {
	if o.Proxmox == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(tfDir, "terraform.tfstate"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	addresses, err := containerAddresses(data)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		return nil
	}
	ids, err := o.Proxmox.ListContainerIDs(ctx, o.Config.Proxmox.Node)
	if err != nil {
		return fmt.Errorf("listing proxmox containers: %w", err)
	}
	exists := make(map[int]bool, len(ids))
	for _, id := range ids {
		exists[id] = true
	}
	for _, a := range addresses {
		if exists[a.vmID] {
			continue
		}
		fmt.Fprintf(o.Out, "LXC %d is gone from Proxmox: forgetting %s.\n", a.vmID, a.address)
		if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"state", "rm", a.address}, Env: env}); err != nil {
			return fmt.Errorf("terraform state rm %s: %w", a.address, err)
		}
	}
	return nil
}

type stateContainer struct {
	address string
	vmID    int
}

// containerAddresses lists the Proxmox containers in a Terraform state
// file (format version 4), with their resource address and VMID.
func containerAddresses(state []byte) ([]stateContainer, error) {
	var s struct {
		Resources []struct {
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				IndexKey   any `json:"index_key"`
				Attributes struct {
					VMID int `json:"vm_id"`
				} `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(state, &s); err != nil {
		return nil, fmt.Errorf("reading terraform state: %w", err)
	}
	var out []stateContainer
	for _, r := range s.Resources {
		if r.Mode != "managed" || r.Type != "proxmox_virtual_environment_container" {
			continue
		}
		for _, inst := range r.Instances {
			address := r.Type + "." + r.Name
			if r.Module != "" {
				address = r.Module + "." + address
			}
			switch k := inst.IndexKey.(type) {
			case string:
				address += "[" + strconv.Quote(k) + "]"
			case float64:
				address += "[" + strconv.Itoa(int(k)) + "]"
			}
			out = append(out, stateContainer{address: address, vmID: inst.Attributes.VMID})
		}
	}
	return out, nil
}
