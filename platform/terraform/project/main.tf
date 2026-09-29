# One LXC per service per environment - see
# docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.
#
# The "../../../modules/lxc" source path assumes this file is
# materialized by urbex plan/apply at exactly
# terraform/projects/<project>/<env>/main.tf, per
# internal/materialize.ProjectTerraform.
module "service" {
  source   = "../../../modules/lxc"
  for_each = var.services

  node_name        = var.node_name
  hostname         = each.key
  vm_id            = each.value.vm_id
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = each.value.cores
  memory_mb        = each.value.memory_mb
  disk_gb          = each.value.disk_gb
  ip_address       = each.value.ip_address
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
}
