package provision

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/sizeparse"
)

type serviceVars struct {
	VMID      int    `json:"vm_id"`
	IPAddress string `json:"ip_address"`
	Cores     int    `json:"cores"`
	MemoryMB  int    `json:"memory_mb"`
	DiskGB    int    `json:"disk_gb"`
}

// tfVars mirrors platform/terraform/project/variables.tf.
// proxmox_api_token is intentionally absent, passed instead via
// TF_VAR_proxmox_api_token so it never touches a file in the GitOps repo.
type tfVars struct {
	ProxmoxAPIURL   string                 `json:"proxmox_api_url"`
	ProxmoxInsecure bool                   `json:"proxmox_insecure"`
	NodeName        string                 `json:"node_name"`
	StoragePool     string                 `json:"storage_pool"`
	TemplateFileID  string                 `json:"template_file_id"`
	SSHPublicKey    string                 `json:"ssh_public_key"`
	Gateway         string                 `json:"gateway"`
	IPPrefixLength  int                    `json:"ip_prefix_length"`
	Services        map[string]serviceVars `json:"services"`
	PoolID          string                 `json:"pool_id"`
	Keyctl          bool                   `json:"keyctl"`
	NetworkBridge   string                 `json:"network_bridge"`
}

func writeTFVars(tfDir string, cfg *platform.Config, project, env string, services []manifest.Service, allocations map[string]allocation.Entry, prefixLen int) error {
	svcVars := make(map[string]serviceVars, len(services))
	for _, svc := range services {
		hostname := Hostname(project, svc.Name, env)
		entry := allocations[hostname]
		resources := svc.ResourcesOrDefault()

		memMB, err := sizeparse.MB(resources.Memory)
		if err != nil {
			return err
		}
		diskGB, err := sizeparse.GB(resources.Disk)
		if err != nil {
			return err
		}

		svcVars[hostname] = serviceVars{
			VMID:      entry.VMID,
			IPAddress: entry.IP,
			Cores:     resources.CPU,
			MemoryMB:  memMB,
			DiskGB:    diskGB,
		}
	}

	vars := tfVars{
		ProxmoxAPIURL:   cfg.Proxmox.APIURL,
		ProxmoxInsecure: cfg.Proxmox.Insecure,
		NodeName:        cfg.Proxmox.Node,
		StoragePool:     cfg.Proxmox.StoragePool,
		TemplateFileID:  cfg.Proxmox.LXCTemplate,
		SSHPublicKey:    cfg.Proxmox.SSHPublicKey,
		Gateway:         cfg.Proxmox.Network.Gateway,
		IPPrefixLength:  prefixLen,
		PoolID:          cfg.Proxmox.Pool,
		Keyctl:          cfg.Proxmox.KeyctlEnabled(),
		NetworkBridge:   cfg.Proxmox.Network.Bridge,
		Services:        svcVars,
	}

	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(tfDir, "terraform.auto.tfvars.json"), data, 0o644)
}
