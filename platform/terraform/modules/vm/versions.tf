# Child modules must declare their non-hashicorp providers too, or
# terraform init looks for hashicorp/proxmox, which doesn't exist.
terraform {
  required_providers {
    proxmox = {
      source = "bpg/proxmox"
    }
  }
}
