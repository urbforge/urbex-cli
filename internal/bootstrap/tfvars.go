package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/platform"
)

// tfVars mirrors platform/terraform/base/variables.tf. proxmox_api_token
// is intentionally absent - it is passed via the TF_VAR_proxmox_api_token
// environment variable so it never touches a file in the GitOps repo.
type tfVars struct {
	ProxmoxAPIURL   string            `json:"proxmox_api_url"`
	ProxmoxInsecure bool              `json:"proxmox_insecure"`
	NodeName        string            `json:"node_name"`
	StoragePool     string            `json:"storage_pool"`
	TemplateFileID  string            `json:"template_file_id"`
	SSHPublicKey    string            `json:"ssh_public_key"`
	VMIDBase        int               `json:"vm_id_base"`
	Gateway         string            `json:"gateway"`
	IPPrefixLength  int               `json:"ip_prefix_length"`
	BaseServiceIPs  map[string]string `json:"base_service_ips"`
	PoolID          string            `json:"pool_id"`
	Keyctl          bool              `json:"keyctl"`
	NetworkBridge   string            `json:"network_bridge"`
	DNSServers      []string          `json:"dns_servers"`
	CloneVMID       int               `json:"clone_vm_id"`
}

// writeTFVars renders terraform.auto.tfvars.json into the base Terraform
// working directory so `terraform apply` picks it up without extra flags.
// cloneVMID is urbex's LXC template to clone new LXCs from, or 0.
func writeTFVars(tfBaseDir string, cfg *platform.Config, serviceIPs map[string]string, prefixLen, cloneVMID int) error {
	vars := tfVars{
		ProxmoxAPIURL:   cfg.Proxmox.APIURL,
		ProxmoxInsecure: cfg.Proxmox.Insecure,
		NodeName:        cfg.Proxmox.Node,
		StoragePool:     cfg.Proxmox.StoragePool,
		TemplateFileID:  cfg.Proxmox.LXCTemplate,
		SSHPublicKey:    cfg.Proxmox.SSHPublicKey,
		VMIDBase:        cfg.Proxmox.VMIDBase,
		Gateway:         cfg.Proxmox.Network.Gateway,
		IPPrefixLength:  prefixLen,
		PoolID:          cfg.Proxmox.Pool,
		Keyctl:          cfg.Proxmox.KeyctlEnabled(),
		NetworkBridge:   cfg.Proxmox.Network.Bridge,
		DNSServers:      cfg.LXCNameservers(serviceIPs[Hostname("technitium")]),
		BaseServiceIPs:  serviceIPs,
		CloneVMID:       cloneVMID,
	}

	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(tfBaseDir, "terraform.auto.tfvars.json")
	return os.WriteFile(path, data, 0o644)
}
