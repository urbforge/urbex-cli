variable "proxmox_api_url" {
  type = string
}

variable "proxmox_api_token" {
  type      = string
  sensitive = true
  # Passed via the TF_VAR_proxmox_api_token environment variable by urbex
  # plan/apply - never written to a file in this repo.
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

variable "gateway" {
  type = string
}

variable "ip_prefix_length" {
  type = number
}

variable "services" {
  description = "Hostname (e.g. my-app-api-staging) => sizing/network for that service's LXC, computed by urbex plan/apply from urbex.yaml and the allocation ledger. The applications with machine: vm are in vms instead."
  type = map(object({
    vm_id      = number
    ip_address = string
    cores      = number
    memory_mb  = number
    disk_gb    = number
    # When it starts as the node boots (see the lxc module).
    startup_order    = number
    startup_up_delay = number
  }))
}

variable "pool_id" {
  type    = string
  default = ""
}

variable "keyctl" {
  type    = bool
  default = false
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

variable "vms" {
  description = "Like services, for the applications that run in a VM (machine: vm, ADR-0033); their startup fields are not used: a VM has no boot order."
  type = map(object({
    vm_id            = number
    ip_address       = string
    cores            = number
    memory_mb        = number
    disk_gb          = number
    startup_order    = number
    startup_up_delay = number
  }))
  default  = {}
  nullable = false
}

variable "vm_clone_vm_id" {
  type        = number
  default     = 0
  description = "VMID of urbex's VM template, which every VM is a linked clone of; 0 when no application runs in a VM."
}
