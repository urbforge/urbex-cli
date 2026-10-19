package platform

import "os"

const templateContent = `domain: example.com

git:
  giteaUrl: https://git.example.com
  org: urbex

# Public endpoints on Cloudflare (optional: leave accountId and zoneId
# empty to keep everything on the LAN). See
# docs/reference/platform-config.md in urbforge/urbex.
cloudflare:
  accountId: ""
  zoneId: ""             # the zone of "domain" above
  # name: urbex          # last label of every hostname
  # hostnames: flat      # flat: auth-urbex.example.com (any plan)
  #                      # nested: auth.urbex.example.com (needs Advanced Certificate Manager)
  # access:
  #   emails: [you@example.com]   # who may open Gitea and Komodo

proxmox:
  apiUrl: https://proxmox.example.internal:8006/api2/json
  insecure: false
  node: pve
  storagePool: local-lvm
  # Debian 13; list what your node has with: pveam list local
  lxcTemplate: local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst
  sshPublicKey: "ssh-ed25519 AAAA... you@example.com"
  vmIdBase: 9000
  # pool: urbex   # required if your API token is scoped to a resource pool
  # keyctl: false # set if the token isn't root@pam (see docs/reference/credentials.md)
  network:
    cidr: 192.168.1.0/24
    gateway: 192.168.1.1
    baseHostOffset: 200 # base services get .200-.204
    bridge: vmbr0
    # dnsServers: [192.168.1.1] # nameservers of the LXCs; default: the Proxmox host's

dns:
  technitiumUrl: http://urbex-technitium.example.internal:5380
  # zone: urbex.example.com # the services' internal names; default: <cloudflare.name>.<domain>
  # forwarders: [192.168.1.1] # resolve everything else; default: network.dnsServers, or the gateway

keycloak:
  baseUrl: https://auth.example.com
  platformRealm: platform

# storage:
#   shares: # folders of the Proxmox node that applications mount by name
#     media:
#       path: /mnt/pve/nas/media
`

// WriteTemplate writes a starter urbex.platform.yaml to path, to be edited
// before running urbex bootstrap again. Credentials never go in this file
// - see docs/decisions/0012-platform-config-and-credentials.md.
func WriteTemplate(path string) error {
	return os.WriteFile(path, []byte(templateContent), 0o644)
}
