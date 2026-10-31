# Base-service LXCs. Fixed VMID offsets and resource sizing for a
# personal-app-scale Proxmox host - see
# docs/decisions/0009-one-lxc-per-service-per-env.md in urbforge/urbex.
# When the node boots, Technitium starts first - every other LXC resolves
# names through it - then the rest of the platform, then the
# applications (startup_order, urbforge/urbex-cli#43).

module "gitea" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-gitea"
  vm_id            = var.vm_id_base + 0
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 20
  ip_address       = var.base_service_ips["urbex-gitea"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 2
  startup_up_delay = 0
}

module "komodo" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-komodo"
  vm_id            = var.vm_id_base + 1
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 10
  ip_address       = var.base_service_ips["urbex-komodo"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 2
  startup_up_delay = 0
}

module "technitium" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-technitium"
  vm_id            = var.vm_id_base + 2
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 1
  memory_mb        = 1024
  disk_gb          = 5
  ip_address       = var.base_service_ips["urbex-technitium"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 1
  startup_up_delay = 10
}

module "keycloak" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-keycloak"
  vm_id            = var.vm_id_base + 3
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 2048
  disk_gb          = 10
  ip_address       = var.base_service_ips["urbex-keycloak"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 2
  startup_up_delay = 0
}

module "observability" {
  source = "../modules/lxc"

  node_name        = var.node_name
  hostname         = "urbex-observability"
  vm_id            = var.vm_id_base + 4
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 2
  memory_mb        = 4096
  disk_gb          = 30
  ip_address       = var.base_service_ips["urbex-observability"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 2
  startup_up_delay = 0
}

# The Cloudflare Tunnel connector: only on a platform that publishes
# endpoints on Cloudflare (urbex puts its address in base_service_ips).
module "tunnel" {
  source = "../modules/lxc"
  count  = contains(keys(var.base_service_ips), "urbex-tunnel") ? 1 : 0

  node_name        = var.node_name
  hostname         = "urbex-tunnel"
  vm_id            = var.vm_id_base + 5
  storage_pool     = var.storage_pool
  template_file_id = var.template_file_id
  clone_vm_id      = var.clone_vm_id
  ssh_public_key   = var.ssh_public_key
  cores            = 1
  memory_mb        = 512
  disk_gb          = 4
  ip_address       = var.base_service_ips["urbex-tunnel"]
  ip_prefix_length = var.ip_prefix_length
  gateway          = var.gateway
  pool_id          = var.pool_id
  keyctl           = var.keyctl
  network_bridge   = var.network_bridge
  dns_servers      = var.dns_servers
  startup_order    = 2
  startup_up_delay = 0
}
