// Package tfstate reconciles Terraform state with what exists on Proxmox.
package tfstate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/urbforge/urbex-cli/internal/runner"
)

// ContainerLister is the subset of proxmoxapi.Client used to tell which
// LXCs still exist.
type ContainerLister interface {
	ListContainerIDs(ctx context.Context, node string) ([]int, error)
}

// ForgetVanished drops from the Terraform state in tfDir the LXCs that no
// longer exist on the Proxmox node. Terraform would notice by itself with
// an unrestricted token, but a token scoped to a resource pool gets "403
// Permission check failed" for a VMID that isn't in the pool - which is
// what a deleted LXC is - so every plan, apply, and destroy would fail on
// it. A nil lister does nothing.
func ForgetVanished(ctx context.Context, lister ContainerLister, node string, run runner.CommandRunner, tfDir string, env []string, out io.Writer) error {
	if lister == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(tfDir, "terraform.tfstate"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	containers, err := Containers(data)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return nil
	}
	ids, err := lister.ListContainerIDs(ctx, node)
	if err != nil {
		return fmt.Errorf("listing proxmox containers: %w", err)
	}
	exists := make(map[int]bool, len(ids))
	for _, id := range ids {
		exists[id] = true
	}
	for _, c := range containers {
		if exists[c.VMID] {
			continue
		}
		fmt.Fprintf(out, "LXC %d is gone from Proxmox: forgetting %s.\n", c.VMID, c.Address)
		if err := run.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"state", "rm", c.Address}, Env: env}); err != nil {
			return fmt.Errorf("terraform state rm %s: %w", c.Address, err)
		}
	}
	return nil
}

// Container is a Proxmox LXC in a Terraform state: its resource address
// and VMID.
type Container struct {
	Address string
	VMID    int
}

// Containers lists the Proxmox containers in a Terraform state file
// (format version 4).
func Containers(state []byte) ([]Container, error) {
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
	var out []Container
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
			out = append(out, Container{Address: address, VMID: inst.Attributes.VMID})
		}
	}
	return out, nil
}
