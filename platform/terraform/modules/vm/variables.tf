variable "node_name" {
  type        = string
  description = "Proxmox node to create the VM on."
}

variable "hostname" {
  type        = string
  description = "VM name, and its hostname."
}

variable "vm_id" {
  type        = number
  description = "Proxmox VMID for the VM."
}

variable "clone_vm_id" {
  type        = number
  description = "VMID of urbex's VM template, which the VM is a linked clone of; 0 only on destroy."
}

variable "cores" {
  type    = number
  default = 1
}

variable "memory_mb" {
  type    = number
  default = 1024
}

variable "disk_gb" {
  type    = number
  default = 8
}

variable "storage_pool" {
  type        = string
  description = "Proxmox storage ID for the VM's disk and its cloud-init drive."
}

variable "ssh_public_key" {
  type        = string
  description = "SSH public key installed for root by cloud-init, used by Ansible to configure the VM."
}

variable "network_bridge" {
  type    = string
  default = "vmbr0"
}

variable "ip_address" {
  type        = string
  description = "Static IPv4 address for the VM."
}

variable "ip_prefix_length" {
  type        = number
  description = "CIDR prefix length matching ip_address, e.g. 24."
}

variable "gateway" {
  type = string
}

variable "pool_id" {
  type        = string
  default     = ""
  description = "Proxmox resource pool to create the VM in; empty for none."
}

variable "dns_servers" {
  type        = list(string)
  default     = []
  nullable    = false
  description = "Nameservers of the VM; empty keeps the template's."
}
