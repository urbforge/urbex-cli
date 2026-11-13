// Package discover answers `urbex discover`: what urbex.platform.yaml
// needs, found on the Proxmox node with the operator's token, on the LAN
// and, with a token, on Cloudflare - then a proposed config built from
// it. It only reads (ADR-0032).
package discover

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// Proxmox is what discover reads of the node (proxmoxapi.Client).
type Proxmox interface {
	Version(ctx context.Context) (string, error)
	Nodes(ctx context.Context) ([]string, error)
	Permissions(ctx context.Context) (map[string]map[string]bool, error)
	Storages(ctx context.Context, node string) ([]proxmoxapi.Storage, error)
	Volumes(ctx context.Context, node, storage, content string) ([]string, error)
	Bridges(ctx context.Context, node string) ([]proxmoxapi.Bridge, error)
	VMIDFree(ctx context.Context, vmid int) (bool, error)
	PoolMembers(ctx context.Context, pool string) ([]proxmoxapi.Member, error)
}

// Cloudflare is what discover reads of the account (cloudflare.Client).
type Cloudflare interface {
	VerifyAccountToken(ctx context.Context, account string) (string, error)
	Zones(ctx context.Context) ([]cloudflare.Zone, error)
}

// Input is where discover looks.
type Input struct {
	APIURL   string // Proxmox's API, https://<host>:8006[/api2/json]
	Insecure bool   // its certificate isn't trusted (Proxmox's own)
	TokenID  string // user@realm!name, the token's public part
	Proxmox  Proxmox
	// Cloudflare is nil without a Cloudflare token.
	Cloudflare Cloudflare
	// Probe reports whether something answers at an address on the LAN;
	// nil skips the scan.
	Probe        func(ctx context.Context, ip string) bool
	SSHPublicKey string
	VMIDBase     int // 9000 unless given
}

// Result is what discover found, and the config it proposes.
type Result struct {
	Proxmox    ProxmoxFound     `json:"proxmox"`
	Network    NetworkFound     `json:"network"`
	Cloudflare *CloudflareFound `json:"cloudflare,omitempty"`
	// SSHPublicKey is the workspace's key, which goes in
	// proxmox.sshPublicKey.
	SSHPublicKey string `json:"sshPublicKey,omitempty"`
	// Proposal is a complete urbex.platform.yaml, with a comment on
	// where each value comes from.
	Proposal string   `json:"proposal,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// ProxmoxFound is the node and what the token may use on it; the chosen
// value of each, with the alternatives.
type ProxmoxFound struct {
	APIURL       string               `json:"apiUrl"`
	Insecure     bool                 `json:"insecure"`
	Version      string               `json:"version,omitempty"`
	TokenID      string               `json:"tokenId,omitempty"`
	Nodes        []string             `json:"nodes,omitempty"`
	Node         string               `json:"node,omitempty"`
	Pool         string               `json:"pool,omitempty"`
	Bridges      []proxmoxapi.Bridge  `json:"bridges,omitempty"`
	Bridge       string               `json:"bridge,omitempty"`
	Storages     []proxmoxapi.Storage `json:"storages,omitempty"`
	StoragePool  string               `json:"storagePool,omitempty"`
	LXCTemplates []string             `json:"lxcTemplates,omitempty"`
	LXCTemplate  string               `json:"lxcTemplate,omitempty"`
	VMIDBase     int                  `json:"vmIdBase"`
	TakenVMIDs   []int                `json:"takenVmIds,omitempty"`
	PoolMembers  []proxmoxapi.Member  `json:"poolMembers,omitempty"`
	// Platform: an Urbex platform is in the pool already.
	Platform bool `json:"existingPlatform,omitempty"`
}

// NetworkFound is the LAN, from the bridge's address on the node, and
// the free block proposed for the platform.
type NetworkFound struct {
	CIDR           string   `json:"cidr,omitempty"`
	Gateway        string   `json:"gateway,omitempty"`
	NodeAddress    string   `json:"nodeAddress,omitempty"`
	Scanned        bool     `json:"scanned"`
	InUse          []string `json:"inUse,omitempty"`
	BaseHostOffset int      `json:"baseHostOffset,omitempty"`
}

// CloudflareFound is the token's zones; one is chosen when there is one.
type CloudflareFound struct {
	TokenActive bool              `json:"tokenActive"`
	Zones       []cloudflare.Zone `json:"zones,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	ZoneID      string            `json:"zoneId,omitempty"`
	AccountID   string            `json:"accountId,omitempty"`
}

// Needed is how many consecutive free addresses the platform's block
// needs: the base services and templates take 10, then the first
// projects' machines.
const Needed = 20

// NormalizeAPIURL makes https://<host>:8006 the API's URL, ending in
// /api2/json.
func NormalizeAPIURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, "/api2/json")
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	if host := strings.SplitN(strings.SplitN(u, "://", 2)[1], "/", 2)[0]; !strings.Contains(host, ":") {
		u = strings.Replace(u, host, host+":8006", 1)
	}
	return u + "/api2/json"
}

// Run looks at the node, the LAN and Cloudflare, and proposes a config.
func Run(ctx context.Context, in Input) *Result {
	r := &Result{SSHPublicKey: strings.TrimSpace(in.SSHPublicKey)}
	r.Proxmox = ProxmoxFound{APIURL: NormalizeAPIURL(in.APIURL), Insecure: in.Insecure, TokenID: in.TokenID, VMIDBase: in.VMIDBase}
	if r.Proxmox.VMIDBase == 0 {
		r.Proxmox.VMIDBase = 9000
	}
	if in.Proxmox != nil && proxmox(ctx, in, r) {
		network(ctx, in, r)
	}
	if in.Cloudflare != nil {
		cloudflareZones(ctx, in.Cloudflare, r)
	}
	if r.SSHPublicKey == "" {
		r.Warnings = append(r.Warnings, "no SSH public key in the workspace (home/.ssh/urbex.pub): 'urbex-op init' creates it")
	}
	if len(r.Errors) == 0 {
		r.Proposal = proposal(r)
	}
	return r
}

var debian13 = regexp.MustCompile(`debian-13-standard_`)

// proxmox reads the node; false when it can't go on.
func proxmox(ctx context.Context, in Input, r *Result) bool {
	p, f := in.Proxmox, &r.Proxmox
	v, err := p.Version(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "401") {
			r.Errors = append(r.Errors, "Proxmox refuses the token (401): check it was stored whole, as user@realm!name=secret")
		} else {
			r.Errors = append(r.Errors, fmt.Sprintf("Proxmox doesn't answer at %s: %v", f.APIURL, err))
		}
		return false
	}
	f.Version = v
	if !strings.HasPrefix(v, "9") {
		r.Warnings = append(r.Warnings, "Proxmox VE "+v+": Urbex is validated on 9")
	}

	if f.Nodes, err = p.Nodes(ctx); err != nil || len(f.Nodes) == 0 {
		r.Errors = append(r.Errors, "the token sees no node")
		return false
	}
	f.Node = f.Nodes[0]
	if len(f.Nodes) > 1 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("several nodes (%s): the values below are %s's - pick one", strings.Join(f.Nodes, ", "), f.Node))
	}

	perms, err := p.Permissions(ctx)
	if err != nil {
		r.Errors = append(r.Errors, "reading the token's permissions: "+err.Error())
		return false
	}
	var pools, sdn []string
	for path := range perms {
		if name, ok := strings.CutPrefix(path, "/pool/"); ok && !strings.Contains(name, "/") {
			pools = append(pools, name)
		}
		if name, ok := strings.CutPrefix(path, "/sdn/zones/localnetwork/"); ok && !strings.Contains(name, "/") {
			sdn = append(sdn, name)
		}
	}
	sort.Strings(pools)
	sort.Strings(sdn)
	root := perms["/"] != nil
	if len(pools) > 0 {
		f.Pool = pools[0]
		if len(pools) > 1 {
			r.Warnings = append(r.Warnings, "the token has several pools ("+strings.Join(pools, ", ")+"): "+f.Pool+" is proposed")
		}
	} else if !root {
		r.Warnings = append(r.Warnings, "the token has no permission on a pool: prepare Proxmox first (docs/reference/credentials.md#proxmox-api-token)")
	}
	if len(sdn) == 0 && !root {
		r.Warnings = append(r.Warnings, "the token has no SDN.Use on a bridge: the LXCs can't get a network (the PVESDNUser line of the preparation)")
	}

	if f.Bridges, err = p.Bridges(ctx, f.Node); err != nil {
		r.Warnings = append(r.Warnings, "reading the node's bridges: "+err.Error())
	}
	f.Bridge = "vmbr0"
	switch {
	case len(sdn) > 0:
		f.Bridge = sdn[0]
	case len(f.Bridges) > 0:
		f.Bridge = f.Bridges[0].Name
	}

	storages, err := p.Storages(ctx, f.Node)
	if err != nil {
		r.Errors = append(r.Errors, "reading the node's storages: "+err.Error())
		return false
	}
	f.Storages = storages
	// Thin, snapshot-capable storage first: what Proxmox installs
	// (local-zfs, local-lvm); then the one with the most room.
	rank := map[string]int{"zfspool": 0, "lvmthin": 1, "rbd": 2, "btrfs": 3, "dir": 5}
	var disks []proxmoxapi.Storage
	for _, s := range storages {
		if s.Active && s.Has("rootdir") && s.Has("images") {
			disks = append(disks, s)
		}
	}
	sort.SliceStable(disks, func(i, j int) bool {
		ri, ok := rank[disks[i].Type]
		if !ok {
			ri = 4
		}
		rj, ok := rank[disks[j].Type]
		if !ok {
			rj = 4
		}
		if ri != rj {
			return ri < rj
		}
		return disks[i].AvailGB > disks[j].AvailGB
	})
	if len(disks) == 0 {
		r.Errors = append(r.Errors, "no storage for LXC and VM disks (rootdir and images) the token can use: add one to the pool ('pveum pool modify <pool> --storage <name>')")
	} else {
		f.StoragePool = disks[0].Name
	}

	for _, s := range storages {
		if !s.Has("vztmpl") {
			continue
		}
		vols, err := p.Volumes(ctx, f.Node, s.Name, "vztmpl")
		if err != nil {
			continue
		}
		for _, v := range vols {
			if debian13.MatchString(v) {
				f.LXCTemplates = append(f.LXCTemplates, v)
			}
		}
	}
	sort.Slice(f.LXCTemplates, func(i, j int) bool { return naturalLess(f.LXCTemplates[i], f.LXCTemplates[j]) })
	// Proxmox nodes are amd64, bar the odd arm64 port: never another
	// architecture's template when ours is there.
	var native []string
	for _, t := range f.LXCTemplates {
		if strings.Contains(t, "_amd64") {
			native = append(native, t)
		}
	}
	if len(native) == 0 {
		native = f.LXCTemplates
	}
	if len(native) > 0 {
		f.LXCTemplate = native[len(native)-1]
	} else {
		r.Errors = append(r.Errors, "no Debian 13 template the token can see: download it on the node as root ('pveam update && pveam available | grep debian-13', then 'pveam download local <it>')")
	}

	// The platform takes vmIdBase to +9, projects from +100.
	for _, id := range []int{f.VMIDBase, f.VMIDBase + 1, f.VMIDBase + 2, f.VMIDBase + 3, f.VMIDBase + 4, f.VMIDBase + 5, f.VMIDBase + 100} {
		if free, err := p.VMIDFree(ctx, id); err == nil && !free {
			f.TakenVMIDs = append(f.TakenVMIDs, id)
		}
	}
	if f.Pool != "" {
		if members, err := p.PoolMembers(ctx, f.Pool); err == nil {
			f.PoolMembers = members
			f.Platform = slices.ContainsFunc(members, func(m proxmoxapi.Member) bool { return m.Name == "urbex-gitea" })
		}
	}
	switch {
	case f.Platform:
		r.Warnings = append(r.Warnings, "an Urbex platform exists in pool "+f.Pool+" already: use its GitOps repo and urbex.platform.yaml rather than a new one")
	case len(f.TakenVMIDs) > 0:
		r.Warnings = append(r.Warnings, fmt.Sprintf("VMIDs taken: %v - choose another proxmox.vmIdBase", f.TakenVMIDs))
	}
	return true
}

// network reads the LAN from the bridge's address on the node, and
// proposes a block of free addresses for the platform.
func network(ctx context.Context, in Input, r *Result) {
	n, f := &r.Network, r.Proxmox
	for _, b := range f.Bridges {
		if b.Name == f.Bridge && b.CIDR != "" {
			ip, ipnet, err := net.ParseCIDR(b.CIDR)
			if err != nil {
				break
			}
			n.CIDR, n.NodeAddress, n.Gateway = ipnet.String(), ip.String(), b.Gateway
		}
	}
	if n.CIDR == "" {
		r.Warnings = append(r.Warnings, "the bridge "+f.Bridge+" has no address on the node: set proxmox.network.cidr and gateway by hand")
		return
	}
	if n.Gateway == "" {
		r.Warnings = append(r.Warnings, "no gateway on the node's bridge: set proxmox.network.gateway by hand")
	}
	_, ipnet, _ := net.ParseCIDR(n.CIDR)
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones < 16 {
		r.Warnings = append(r.Warnings, "the LAN "+n.CIDR+" is not an IPv4 /16 to /30: choose the block by hand")
		return
	}
	// Offsets are within the last octet's /24 around the node, as the
	// docs' examples are; a bigger LAN keeps the node's /24.
	base := ipnet.IP.To4()
	if ones < 24 {
		node := net.ParseIP(n.NodeAddress).To4()
		base = net.IPv4(node[0], node[1], node[2], 0).To4()
	}
	last := 254
	if ones > 24 {
		last = (1 << (32 - ones)) - 2
	}
	used := map[int]bool{}
	for _, a := range []string{n.NodeAddress, n.Gateway} {
		if ip := net.ParseIP(a).To4(); ip != nil && ip[2] == base[2] {
			used[int(ip[3])] = true
		}
	}
	if in.Probe != nil {
		n.Scanned = true
		type hit struct {
			host int
			up   bool
		}
		hits := make(chan hit)
		sem := make(chan struct{}, 64)
		for h := 1; h <= last; h++ {
			go func(h int) {
				sem <- struct{}{}
				defer func() { <-sem }()
				ip := net.IPv4(base[0], base[1], base[2], base[3]+byte(h))
				hits <- hit{h, in.Probe(ctx, ip.String())}
			}(h)
		}
		for i := 1; i <= last; i++ {
			if h := <-hits; h.up {
				used[h.host] = true
			}
		}
	} else {
		r.Warnings = append(r.Warnings, "the LAN wasn't scanned: check the proposed block is free and outside the DHCP range")
	}
	for h := range used {
		n.InUse = append(n.InUse, net.IPv4(base[0], base[1], base[2], base[3]+byte(h)).String())
	}
	sort.Slice(n.InUse, func(i, j int) bool { return naturalLess(n.InUse[i], n.InUse[j]) })

	// The block nearest the docs' default (.200), going down then up.
	free := func(from int) bool {
		if from < 2 || from+Needed-1 > last {
			return false
		}
		for h := from; h < from+Needed; h++ {
			if used[h] {
				return false
			}
		}
		return true
	}
	for d := 0; d <= last; d++ {
		for _, from := range []int{200 - d, 200 + d} {
			if free(from) {
				n.BaseHostOffset = from
				break
			}
		}
		if n.BaseHostOffset != 0 {
			break
		}
	}
	if n.BaseHostOffset == 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("no %d consecutive free addresses on %s: free a block, or choose baseHostOffset by hand", Needed, n.CIDR))
	} else if n.Scanned {
		r.Warnings = append(r.Warnings, "the scan sees only what answers (ping, or a TCP port): keep the proposed block outside the DHCP range")
	}
}

func cloudflareZones(ctx context.Context, c Cloudflare, r *Result) {
	f := &CloudflareFound{}
	r.Cloudflare = f
	zones, err := c.Zones(ctx)
	if err != nil {
		r.Errors = append(r.Errors, "Cloudflare refuses the token on /zones: it needs Zone Read - "+err.Error())
		return
	}
	f.Zones = zones
	switch len(zones) {
	case 0:
		r.Errors = append(r.Errors, "the Cloudflare token sees no zone")
	case 1:
		f.Domain, f.ZoneID, f.AccountID = zones[0].Name, zones[0].ID, zones[0].Account.ID
	default:
		f.Domain, f.ZoneID, f.AccountID = zones[0].Name, zones[0].ID, zones[0].Account.ID
		var names []string
		for _, z := range zones {
			names = append(names, z.Name)
		}
		r.Warnings = append(r.Warnings, "several zones ("+strings.Join(names, ", ")+"): "+f.Domain+" is proposed - pick the platform's domain")
	}
	// The token is verified against the zone's account: the docs make an
	// account token.
	status, err := c.VerifyAccountToken(ctx, f.AccountID)
	f.TokenActive = err == nil && status == "active"
	if !f.TokenActive {
		r.Warnings = append(r.Warnings, "the Cloudflare token isn't active: check it, or create it under Account API tokens (docs/reference/credentials.md#cloudflare-api-token)")
	}
}

// naturalLess orders strings with their numbers as numbers: 13.6 after 13.10
// is wrong, 192.168.1.9 before 192.168.1.10 right.
func naturalLess(a, b string) bool {
	split := regexp.MustCompile(`\d+|\D+`)
	pa, pb := split.FindAllString(a, -1), split.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil {
			return na < nb
		}
		return pa[i] < pb[i]
	}
	return len(pa) < len(pb)
}
