# Base-service LXCs. Fixed VMID offsets and resource sizing for a
# personal-app-scale Proxmox host - see
# docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.

module "gitea" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-gitea"
  vm_id            = var.vm_id_base + 0
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 20
  ip_address       = var.base_service_ips["urbex-gitea"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
}

module "komodo" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-komodo"
  vm_id            = var.vm_id_base + 1
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 10
  ip_address       = var.base_service_ips["urbex-komodo"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
}

module "technitium" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-technitium"
  vm_id            = var.vm_id_base + 2
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = 1
  memory_mb        = 512
  disk_gb          = 5
  ip_address       = var.base_service_ips["urbex-technitium"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
}

module "keycloak" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-keycloak"
  vm_id            = var.vm_id_base + 3
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 10
  ip_address       = var.base_service_ips["urbex-keycloak"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
}

module "observability" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-observability"
  vm_id            = var.vm_id_base + 4
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 4096
  disk_gb          = 30
  ip_address       = var.base_service_ips["urbex-observability"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
}
