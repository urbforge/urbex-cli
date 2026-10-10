variable "proxmox_api_url" {
  type = string
}

variable "proxmox_api_token" {
  type      = string
  sensitive = true
  # Passed via the TF_VAR_proxmox_api_token environment variable by urbex
  # bootstrap - never written to a file in this repo.
}

variable "proxmox_insecure" {
  type    = bool
  default = false
}

variable "node_name" {
  type = string
}

variable "storage_pool" {
  type = string
}

variable "template_file_id" {
  type = string
}

variable "ssh_public_key" {
  type = string
}

variable "vm_id_base" {
  type        = number
  default     = 9000
  description = "First VMID used for base-service containers; each service gets vm_id_base + its fixed offset."
}

variable "gateway" {
  type = string
}

variable "ip_prefix_length" {
  type = number
}

variable "base_service_ips" {
  type        = map(string)
  description = "Hostname (e.g. urbex-gitea) => statically allocated IPv4 address, computed by urbex bootstrap via internal/netalloc."
}

variable "pool_id" {
  type    = string
  default = ""
}

variable "keyctl" {
  type    = bool
  default = true
}

variable "network_bridge" {
  type    = string
  default = "vmbr0"
}

variable "dns_servers" {
  type        = list(string)
  default     = []
  nullable    = false
  description = "Nameservers of the LXCs; empty copies the Proxmox host's."
}

variable "clone_vm_id" {
  type        = number
  default     = 0
  description = "VMID of urbex's prepared LXC template; 0 creates containers from template_file_id."
}
