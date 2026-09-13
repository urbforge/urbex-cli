variable "node_name" {
  type        = string
  description = "Proxmox node to create the container on."
}

variable "hostname" {
  type        = string
  description = "Container hostname."
}

variable "vm_id" {
  type        = number
  description = "Proxmox VMID for the container."
}

variable "cores" {
  type    = number
  default = 2
}

variable "memory_mb" {
  type    = number
  default = 2048
}

variable "disk_gb" {
  type    = number
  default = 10
}

variable "storage_pool" {
  type        = string
  description = "Proxmox storage ID used for the container's root disk."
}

variable "template_file_id" {
  type        = string
  description = "Proxmox storage ID of the LXC template (e.g. a Debian 12 template)."
}

variable "ssh_public_key" {
  type        = string
  description = "SSH public key installed for the root user, used by Ansible to configure the container."
}

variable "network_bridge" {
  type    = string
  default = "vmbr0"
}

variable "ip_address" {
  type        = string
  description = "Static IPv4 address for the container (see internal/netalloc)."
}

variable "ip_prefix_length" {
  type        = number
  description = "CIDR prefix length matching ip_address, e.g. 24."
}

variable "gateway" {
  type = string
}
