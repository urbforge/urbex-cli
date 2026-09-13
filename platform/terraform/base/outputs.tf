output "hostnames" {
  value = {
    gitea         = module.gitea.hostname
    komodo        = module.komodo.hostname
    technitium    = module.technitium.hostname
    keycloak      = module.keycloak.hostname
    observability = module.observability.hostname
  }
}

output "ip_addresses" {
  value = {
    gitea         = module.gitea.ip_address
    komodo        = module.komodo.ip_address
    technitium    = module.technitium.ip_address
    keycloak      = module.keycloak.ip_address
    observability = module.observability.ip_address
  }
}
