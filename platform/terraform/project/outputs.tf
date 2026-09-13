output "hostnames" {
  value = keys(var.services)
}

output "ip_addresses" {
  value = { for hostname, mod in module.service : hostname => mod.ip_address }
}
