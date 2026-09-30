package platform

import "os"

const templateContent = `domain: example.com

git:
  giteaUrl: https://git.example.com
  org: urbex

cloudflare:
  accountId: ""
  zoneId: ""
  tunnelName: urbex

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
  # keyctl: false # set if the token isn't root@pam (see docs/credentials.md)
  network:
    cidr: 192.168.1.0/24
    gateway: 192.168.1.1
    baseHostOffset: 200 # base services get .200-.204
    bridge: vmbr0

dns:
  technitiumUrl: http://urbex-technitium.example.internal:5380

keycloak:
  baseUrl: https://auth.example.com
  platformRealm: platform
`

// WriteTemplate writes a starter urbex.platform.yaml to path, to be edited
// before running urbex bootstrap again. Credentials never go in this file
// - see docs/decisions/0012-platform-config-and-credentials.md.
func WriteTemplate(path string) error {
	return os.WriteFile(path, []byte(templateContent), 0o644)
}
