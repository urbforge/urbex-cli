# Reusable module: one unprivileged Proxmox LXC container with Docker
# support (nesting enabled) and a static IPv4 address, ready for Ansible
# to configure over SSH. See docs/decisions/0017-embedded-base-infra-assets.md
# in urbforge/urbex.

resource "proxmox_virtual_environment_container" "this" {
  node_name = var.node_name
  vm_id     = var.vm_id

  unprivileged = true
  started      = true

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

  # Required for Docker (and docker compose) to run inside the LXC.
  features {
    nesting = true
  }
}
