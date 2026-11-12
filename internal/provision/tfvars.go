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
	// When the LXC starts as the node boots (startupOrder).
	StartupOrder   int `json:"startup_order"`
	StartupUpDelay int `json:"startup_up_delay"`
}

// The applications' LXCs start, when the node boots, after the
// platform's (1 and 2, platform/terraform/base): at appStartupOrder plus
// their depth in the graph of `uses`, so that each starts after what it
// uses. One that others use holds the next back for its ready_within, at
// most maxStartupUpDelay: Proxmox waits that long after each LXC it
// starts (urbforge/urbex-cli#43).
const (
	appStartupOrder   = 10
	maxStartupUpDelay = 300
)

// startupOrder returns the boot order and delay of svc's LXC.
func startupOrder(m *manifest.Manifest, svc manifest.Service) (order, upDelay int) {
	for _, other := range m.Services {
		for _, used := range other.Uses {
			if used == svc.Name {
				upDelay = min(svc.ReadySeconds(), maxStartupUpDelay)
			}
		}
	}
	return appStartupOrder + m.Depth(svc.Name), upDelay
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
	VMs             map[string]serviceVars `json:"vms"`
	PoolID          string                 `json:"pool_id"`
	Keyctl          bool                   `json:"keyctl"`
	NetworkBridge   string                 `json:"network_bridge"`
	DNSServers      []string               `json:"dns_servers"`
	CloneVMID       int                    `json:"clone_vm_id"`
	VMCloneVMID     int                    `json:"vm_clone_vm_id"`
}

// writeTFVars renders the project's terraform.auto.tfvars.json;
// nameservers are its LXCs' (Technitium, then its forwarders). The
// applications with machine: vm go in vms, cloned from vmCloneVMID.
func writeTFVars(tfDir string, cfg *platform.Config, project, env string, services []manifest.Service, allocations map[string]allocation.Entry, prefixLen int, nameservers []string, cloneVMID, vmCloneVMID int) error {
	svcVars := make(map[string]serviceVars, len(services))
	vmVars := map[string]serviceVars{}
	graph := &manifest.Manifest{Services: services}
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

		order, upDelay := startupOrder(graph, svc)
		target := svcVars
		if svc.IsVM() {
			target = vmVars
		}
		target[hostname] = serviceVars{
			VMID:           entry.VMID,
			IPAddress:      entry.IP,
			Cores:          resources.CPU,
			MemoryMB:       memMB,
			DiskGB:         diskGB,
			StartupOrder:   order,
			StartupUpDelay: upDelay,
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
		DNSServers:      nameservers,
		Services:        svcVars,
		VMs:             vmVars,
		CloneVMID:       cloneVMID,
		VMCloneVMID:     vmCloneVMID,
	}

	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(tfDir, "terraform.auto.tfvars.json"), data, 0o644)
}
