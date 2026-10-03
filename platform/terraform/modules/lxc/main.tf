# Reusable module: one unprivileged Proxmox LXC container with Docker
# support (nesting enabled) and a static IPv4 address, ready for Ansible
# to configure over SSH. See docs/decisions/0017-embedded-base-infra-assets.md
# in urbforge/urbex.

resource "proxmox_virtual_environment_container" "this" {
  node_name = var.node_name
  vm_id     = var.vm_id

  unprivileged  = true
  started       = true
  start_on_boot = true

  # Empty means "no pool". On a Proxmox where the API token is scoped to a
  # pool (e.g. PVEVMAdmin on /pool/urbex), containers must be created in
  # that pool or the token can't see or manage them afterwards.
  pool_id = var.pool_id != "" ? var.pool_id : null

  initialization {
    hostname = var.hostname

    ip_config {
      ipv4 {
        address = "${var.ip_address}/${var.ip_prefix_length}"
        gateway = var.gateway
      }
    }

    user_account {
      keys = [var.ssh_public_key]
    }

    # Without servers, the LXC copies the Proxmox host's resolv.conf - which
    # is wrong when the host resolves through something the LXCs can't
    # reach (Tailscale's 100.100.100.100, a local stub resolver).
    dynamic "dns" {
      for_each = length(var.dns_servers) > 0 ? [1] : []
      content {
        servers = var.dns_servers
      }
    }
  }

  disk {
    datastore_id = var.storage_pool
    size         = var.disk_gb
  }

  cpu {
    cores = var.cores
  }

  memory {
    dedicated = var.memory_mb
  }

  network_interface {
    name   = "eth0"
    bridge = var.network_bridge
  }

  operating_system {
    template_file_id = var.template_file_id
    type             = "debian"
  }

  # nesting is required for Docker (and docker compose) to run inside the
  # LXC; keyctl too on most hosts, or overlayfs mounts fail with
  # "permission denied". Proxmox only lets root@pam change feature flags
  # other than nesting, so a non-root API token must set keyctl = false
  # (platform config proxmox.keyctl) and have it enabled another way.
  features {
    nesting = true
    keyctl  = var.keyctl
  }
}
