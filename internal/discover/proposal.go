package discover

import (
	"fmt"
	"strings"
)

// proposal writes urbex.platform.yaml from what was found, saying where
// each value comes from. What discover can't know is marked to fill in.
func proposal(r *Result) string {
	p, n := r.Proxmox, r.Network
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	domain, domainNote := "home.arpa", "no Cloudflare zone: the internal names are <service>.urbex.home.arpa (RFC 8375); set your own domain if you have one"
	cf := r.Cloudflare
	if cf != nil && cf.Domain != "" {
		domain, domainNote = cf.Domain, "the Cloudflare zone"
	}
	w("# Proposed by 'urbex discover' - check every value, then run 'urbex bootstrap'.")
	w("# See docs/reference/platform-config.md in urbforge/urbex.")
	w("domain: %s   # %s", domain, domainNote)
	w("")
	w("git:")
	w("  giteaUrl: https://git-urbex.%s", domain)
	w("  org: urbex")
	w("")
	if cf != nil && cf.ZoneID != "" {
		w("cloudflare:   # public HTTPS on %s; Gitea and Komodo behind Cloudflare Access", domain)
		w("  accountId: %s", cf.AccountID)
		w("  zoneId: %s", cf.ZoneID)
		w("  access:")
		w("    emails: [you@example.com]   # FILL IN: who may open Gitea and Komodo (a one-time PIN goes to the address)")
		w("")
	} else {
		w("# No Cloudflare: everything stays on the LAN. For public addresses without")
		w("# an account, uncomment:")
		w("# cloudflare:")
		w("#   quickTunnel: true")
		w("")
	}
	w("proxmox:")
	w("  apiUrl: %s", p.APIURL)
	if p.Insecure {
		w("  insecure: true   # the node's certificate is Proxmox's own, self-signed")
	} else {
		w("  insecure: false")
	}
	w("  node: %s", p.Node)
	w("  storagePool: %s   # %s", p.StoragePool, storageNote(p))
	w("  lxcTemplate: %s", p.LXCTemplate)
	if r.SSHPublicKey != "" {
		w("  sshPublicKey: %q   # the workspace's home/.ssh/urbex.pub", r.SSHPublicKey)
	} else {
		w("  sshPublicKey: \"\"   # FILL IN: the public key Ansible reaches the LXCs with ('urbex-op init' makes home/.ssh/urbex.pub)")
	}
	note := "free"
	if len(p.TakenVMIDs) > 0 {
		note = fmt.Sprintf("TAKEN: %v - choose another", p.TakenVMIDs)
	}
	w("  vmIdBase: %d   # base services from here, projects from +100: %s", p.VMIDBase, note)
	if p.Pool != "" {
		w("  pool: %s   # the token's pool", p.Pool)
	}
	w("  network:")
	if n.CIDR != "" {
		w("    cidr: %s   # bridge %s on %s", n.CIDR, p.Bridge, p.Node)
	} else {
		w("    cidr: \"\"   # FILL IN: the LAN, e.g. 192.168.1.0/24")
	}
	if n.Gateway != "" {
		w("    gateway: %s", n.Gateway)
	} else {
		w("    gateway: \"\"   # FILL IN")
	}
	switch {
	case n.BaseHostOffset != 0 && n.Scanned:
		w("    baseHostOffset: %d   # .%d-.%d answered nothing in the scan; keep them out of the DHCP range", n.BaseHostOffset, n.BaseHostOffset, n.BaseHostOffset+Needed-1)
	case n.BaseHostOffset != 0:
		w("    baseHostOffset: %d   # not scanned: check .%d-.%d are free and out of the DHCP range", n.BaseHostOffset, n.BaseHostOffset, n.BaseHostOffset+Needed-1)
	default:
		w("    baseHostOffset: 200   # FILL IN: the first of %d free addresses", Needed)
	}
	w("    bridge: %s", p.Bridge)
	if n.Gateway != "" {
		w("    dnsServers: [%s]   # the gateway; set your LAN's resolver if it isn't", n.Gateway)
	}
	return b.String()
}

func storageNote(p ProxmoxFound) string {
	for _, s := range p.Storages {
		if s.Name == p.StoragePool {
			return fmt.Sprintf("%s, %d of %d GB free", s.Type, s.AvailGB, s.TotalGB)
		}
	}
	return "for the LXCs' and VMs' disks"
}
