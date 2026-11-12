output "hostnames" {
  value = concat(keys(var.services), keys(var.vms))
}

output "ip_addresses" {
  value = merge(
    { for hostname, mod in module.service : hostname => mod.ip_address },
    { for hostname, mod in module.vm : hostname => mod.ip_address },
  )
}
