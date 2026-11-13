# Reusable module: one Proxmox VM for an application that needs what an
# unprivileged LXC can't do - keyctl, mount, fuse - a linked clone of
# urbex's VM template, configured by cloud-init with a static IPv4
# address and the platform's SSH key, ready for Ansible like an LXC. See
# docs/decisions/0033-vm-for-keyctl-mount-fuse.md in urbforge/urbex.

resource "proxmox_virtual_environment_vm" "this" {
  node_name = var.node_name
  vm_id     = var.vm_id
  name      = var.hostname

  started = true
  on_boot = true

  # No boot order: Proxmox lets only a user with Sys.Modify on / set a
  # VM's startup (an LXC's needs no more than the pool), so a VM starts
  # with the unordered machines, after the platform and the ordered LXCs.
  # Its Stack waits for what it uses all the same (ADR-0031).

  # Empty means "no pool"; a pool-scoped token only sees what is in its
  # pool (see the lxc module).
  pool_id = var.pool_id != "" ? var.pool_id : null

  # A linked clone of the VM template. Only destroy runs without one
  # (clone_vm_id 0): the provider refuses a clone of VMID 0 even then,
  # and where a VM came from is ignored once it exists (lifecycle).
  dynamic "clone" {
    for_each = var.clone_vm_id > 0 ? [1] : []
    content {
      vm_id = var.clone_vm_id
      full  = false
    }
  }

  # The template has the QEMU guest agent: Proxmox shuts the VM down
  # through it.
  agent {
    enabled = true
  }

  operating_system {
    type = "l26"
  }

  # Proxmox's default type, so that the VM can move to another node.
  cpu {
    cores = var.cores
    type  = "x86-64-v2-AES"
  }

  # Ballooning down to half: an idle VM gives memory back to the node.
  memory {
    dedicated = var.memory_mb
    floating  = floor(var.memory_mb / 2)
  }

  scsi_hardware = "virtio-scsi-single"

  # The template's disk, grown to the application's size. Every attribute
  # that differs from the provider's defaults is set, or a clone would
  # lose it (the provider's docs, "Cloning").
  disk {
    datastore_id = var.storage_pool
    interface    = "scsi0"
    size         = var.disk_gb
    discard      = "on"
    iothread     = true
  }

  network_device {
    bridge = var.network_bridge
  }

  initialization {
    datastore_id = var.storage_pool

    ip_config {
      ipv4 {
        address = "${var.ip_address}/${var.ip_prefix_length}"
        gateway = var.gateway
      }
    }

    dynamic "dns" {
      for_each = length(var.dns_servers) > 0 ? [1] : []
      content {
        servers = var.dns_servers
      }
    }

    # Ansible connects as root, as on an LXC.
    user_account {
      username = "root"
      keys     = [var.ssh_public_key]
    }
  }

  stop_on_destroy = true

  # Where a VM came from only matters when it is created: a new template
  # must never replace the VMs that exist.
  lifecycle {
    ignore_changes = [clone]
  }
}
