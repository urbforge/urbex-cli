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

  # The order of the LXCs when the node boots: the platform first, then
  # the applications, each after what it uses (urbforge/urbex-cli#43).
  dynamic "startup" {
    for_each = var.startup_order > 0 ? [1] : []
    content {
      order    = var.startup_order
      up_delay = var.startup_up_delay
    }
  }

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

    # Proxmox only takes SSH keys when it creates an LXC from a container
    # template: a clone has them already, from urbex's template.
    dynamic "user_account" {
      for_each = var.clone_vm_id > 0 ? [] : [1]
      content {
        keys = [var.ssh_public_key]
      }
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

  # From urbex's prepared template when there is one, Docker and the
  # agents' tools already installed (redemptor#18) - a linked clone, made
  # in seconds; the template can't be deleted while clones of it exist,
  # so a newer template gets a VMID of its own. Else from the Debian
  # container template.
  dynamic "clone" {
    for_each = var.clone_vm_id > 0 ? [1] : []
    content {
      vm_id = var.clone_vm_id
      full  = false
    }
  }

  dynamic "operating_system" {
    for_each = var.clone_vm_id > 0 ? [] : [1]
    content {
      template_file_id = var.template_file_id
      type             = "debian"
    }
  }

  # nesting is required for Docker (and docker compose) to run inside the
  # LXC; keyctl too on some hosts, or overlayfs mounts fail with
  # "permission denied". Proxmox only lets root@pam change feature flags
  # other than nesting, so keyctl is off unless the platform config sets
  # proxmox.keyctl: true, with a root@pam token. A change is made in
  # place, and takes effect when the LXC next restarts.
  features {
    nesting = true
    keyctl  = var.keyctl
  }

  # Where an LXC came from only matters when it is created: a new template
  # (or none) must never replace the LXCs that exist. The mount points
  # are urbex's (the applications' volumes) and the operator's (shares),
  # added through the API and pct: the provider replaces the LXC on any
  # change to them, and its volumes with it (ADR-0028).
  lifecycle {
    ignore_changes = [clone, operating_system, initialization[0].user_account, mount_point]
  }
}
