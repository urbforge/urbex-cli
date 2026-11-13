output "hostname" {
  value = var.hostname
}

output "ip_address" {
  value = var.ip_address
}

output "vm_id" {
  value = proxmox_virtual_environment_vm.this.vm_id
}
