// Package platform loads, validates, and scaffolds urbex.platform.yaml,
// the platform-wide (non-sensitive) configuration used by urbex bootstrap.
// See docs/decisions/0012-platform-config-and-credentials.md in
// urbforge/urbex.
package platform

import (
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed content of urbex.platform.yaml.
type Config struct {
	Domain     string           `yaml:"domain"`
	Git        GitConfig        `yaml:"git"`
	Cloudflare CloudflareConfig `yaml:"cloudflare"`
	Proxmox    ProxmoxConfig    `yaml:"proxmox"`
	DNS        DNSConfig        `yaml:"dns"`
	Keycloak   KeycloakConfig   `yaml:"keycloak"`
	Storage    StorageConfig    `yaml:"storage,omitempty"`
}

// StorageConfig is storage that exists outside Urbex (ADR-0028).
type StorageConfig struct {
	// Shares are folders the operator provides - a NAS mounted on the
	// Proxmox node - that applications mount by name.
	Shares map[string]Share `yaml:"shares,omitempty"`
}

// Share is a folder on the Proxmox node, at Path.
type Share struct {
	Path string `yaml:"path"`
}

type GitConfig struct {
	GiteaURL string `yaml:"giteaUrl"`
	Org      string `yaml:"org"`
}

// CloudflareConfig is where the platform's public endpoints live: the
// account and zone (of Config.Domain) Urbex creates its tunnel, DNS
// records, Access applications and Workers in. See
// docs/decisions/0022-cloudflare-tunnel-access-workers.md in urbforge/urbex.
type CloudflareConfig struct {
	AccountID string `yaml:"accountId"`
	ZoneID    string `yaml:"zoneId"`
	// TunnelName is the tunnel Urbex creates and routes through (default
	// "<name>-platform").
	TunnelName string `yaml:"tunnelName"`
	// Name is the last label of every hostname Urbex publishes, telling
	// them apart from anything else in the zone (default "urbex").
	Name string `yaml:"name,omitempty"`
	// Hostnames is how labels are joined: "flat" (auth-urbex.example.com,
	// covered by Cloudflare's free certificate) or "nested"
	// (auth.urbex.example.com, which needs Advanced Certificate Manager).
	Hostnames string `yaml:"hostnames,omitempty"`
	// Access lists who may reach the protected platform services.
	Access AccessConfig `yaml:"access,omitempty"`
	// QuickTunnel starts a Cloudflare Quick Tunnel (cloudflared tunnel
	// --url, no account, domain or token needed) on the LXC of every
	// manifest service marked public: true, instead of routing it
	// through AccountID/ZoneID's named tunnel. A stand-in for when
	// Cloudflare isn't configured yet, not a production substitute: the
	// hostname is random and changes on every restart, the tunnel
	// doesn't always recover from a network interruption, and there is
	// no authentication in front of the service. See
	// docs/decisions/0025-cloudflare-quick-tunnel.md in urbforge/urbex.
	QuickTunnel bool `yaml:"quickTunnel,omitempty"`
}

// AccessConfig is who Cloudflare Access lets through to Gitea and Komodo.
type AccessConfig struct {
	Emails []string `yaml:"emails,omitempty"`
}

// Enabled reports whether the platform publishes endpoints on Cloudflare.
func (c CloudflareConfig) Enabled() bool {
	return c.AccountID != "" && c.ZoneID != ""
}

// Hostname builds a public hostname from labels, most specific first -
// ("auth") for Keycloak, ("api", "staging", "hello") for a service - then
// the platform's name, under domain.
func (c CloudflareConfig) Hostname(domain string, labels ...string) string {
	labels = append(append([]string{}, labels...), c.Name)
	sep := "-"
	if c.Hostnames == HostnamesNested {
		sep = "."
	}
	return strings.Join(labels, sep) + "." + domain
}

// Hostname schemes (CloudflareConfig.Hostnames).
const (
	HostnamesFlat   = "flat"
	HostnamesNested = "nested"
)

type ProxmoxConfig struct {
	APIURL       string        `yaml:"apiUrl"`
	Insecure     bool          `yaml:"insecure"`
	Node         string        `yaml:"node"`
	StoragePool  string        `yaml:"storagePool"`
	LXCTemplate  string        `yaml:"lxcTemplate"`
	SSHPublicKey string        `yaml:"sshPublicKey"`
	VMIDBase     int           `yaml:"vmIdBase"`
	Network      NetworkConfig `yaml:"network"`
	// Pool is the Proxmox resource pool every LXC is created in. Required
	// when the API token's permissions are scoped to a pool; empty for none.
	Pool string `yaml:"pool,omitempty"`
	// Keyctl enables the LXC keyctl feature, which Docker needs on some
	// hosts. Proxmox only lets root@pam set it, so it is off unless set to
	// true: the default works with a non-root, pool-scoped token.
	Keyctl *bool `yaml:"keyctl,omitempty"`
	// VMImage is the Debian 13 cloud image, on a storage with the import
	// content type, that urbex's VM template is built from; required
	// only once an application asks for a VM (ADR-0033).
	VMImage string `yaml:"vmImage,omitempty"`
}

// KeyctlEnabled reports whether LXCs get the keyctl feature (default false).
func (p ProxmoxConfig) KeyctlEnabled() bool {
	return p.Keyctl != nil && *p.Keyctl
}

// NetworkConfig describes the network base-service LXCs are attached to.
// Base services get static IPs deterministically allocated from CIDR,
// starting at BaseHostOffset - see internal/netalloc.
type NetworkConfig struct {
	CIDR           string `yaml:"cidr"`
	Gateway        string `yaml:"gateway"`
	BaseHostOffset int    `yaml:"baseHostOffset"`
	// Bridge is the Proxmox bridge LXCs attach to (default vmbr0).
	Bridge string `yaml:"bridge,omitempty"`
	// DNSServers are the LXCs' nameservers. Empty: they copy the Proxmox
	// host's resolv.conf, which fails when the host resolves through
	// something the LXCs can't reach (e.g. Tailscale's MagicDNS).
	DNSServers []string `yaml:"dnsServers,omitempty"`
}

// DNSConfig is the platform's internal DNS, served by Technitium. See
// docs/decisions/0027-service-conventions.md in urbforge/urbex.
type DNSConfig struct {
	TechnitiumURL string `yaml:"technitiumUrl"`
	// Zone is the zone Technitium serves the services' internal names in
	// (default "<cloudflare.name>.<domain>": the platform's own subdomain).
	Zone string `yaml:"zone,omitempty"`
	// Forwarders resolve every name outside Zone (default
	// proxmox.network.dnsServers, or the gateway when that is empty).
	Forwarders []string `yaml:"forwarders,omitempty"`
}

// InternalZone is the zone the services' internal names live in.
func (c *Config) InternalZone() string {
	if c.DNS.Zone != "" {
		return strings.TrimSuffix(c.DNS.Zone, ".")
	}
	name := c.Cloudflare.Name
	if name == "" {
		name = "urbex"
	}
	return name + "." + c.Domain
}

// InternalName builds an internal name from labels, most specific first -
// ("git") for Gitea, ("api", "staging", "hello") for a service: the
// nested form of the public hostnames, under InternalZone.
func (c *Config) InternalName(labels ...string) string {
	return strings.Join(append(append([]string{}, labels...), c.InternalZone()), ".")
}

// ServiceInternalName is a project service's internal name:
// <service>.<project>.<zone> in prod, <service>.<env>.<project>.<zone>
// elsewhere.
func (c *Config) ServiceInternalName(project, env, service string) string {
	labels := []string{service}
	if env != "prod" {
		labels = append(labels, env)
	}
	return c.InternalName(append(labels, project)...)
}

// DNSForwarders are where Technitium sends every name outside its zone.
func (c *Config) DNSForwarders() []string {
	if len(c.DNS.Forwarders) > 0 {
		return append([]string{}, c.DNS.Forwarders...)
	}
	if len(c.Proxmox.Network.DNSServers) > 0 {
		return append([]string{}, c.Proxmox.Network.DNSServers...)
	}
	return []string{c.Proxmox.Network.Gateway}
}

// LXCNameservers are the nameservers of every LXC, given Technitium's
// address: Technitium first, then its forwarders, so public names keep
// resolving when Technitium is down.
func (c *Config) LXCNameservers(technitiumIP string) []string {
	servers := []string{technitiumIP}
	for _, f := range c.DNSForwarders() {
		if f != technitiumIP {
			servers = append(servers, f)
		}
	}
	return servers
}

// PlatformLabel is the internal name of one of the platform's services.
type PlatformLabel struct {
	Label   string // under InternalZone: "git", "auth", ...
	Service string // the base service answering it: "gitea", "keycloak", ...
}

// PlatformLabels are the platform services' internal names - the labels
// of their public hostnames where they have one - from the service
// catalog. No project may take one as its name.
var PlatformLabels = labels() // from the service catalog

// ReservedLabel reports whether name is one of PlatformLabels.
func ReservedLabel(name string) bool {
	for _, l := range PlatformLabels {
		if l.Label == name {
			return true
		}
	}
	return false
}

type KeycloakConfig struct {
	BaseURL       string `yaml:"baseUrl"`
	PlatformRealm string `yaml:"platformRealm"`
}

// LoadFile reads and parses a urbex.platform.yaml file, filling in
// defaults for optional fields.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Proxmox.StoragePool == "" {
		cfg.Proxmox.StoragePool = "local-lvm"
	}
	if cfg.Proxmox.VMIDBase == 0 {
		cfg.Proxmox.VMIDBase = 9000
	}
	if cfg.Proxmox.Network.BaseHostOffset == 0 {
		cfg.Proxmox.Network.BaseHostOffset = 200
	}
	if cfg.Git.Org == "" {
		cfg.Git.Org = "urbex"
	}
	if cfg.Proxmox.Network.Bridge == "" {
		cfg.Proxmox.Network.Bridge = "vmbr0"
	}
	if cfg.Cloudflare.Name == "" {
		cfg.Cloudflare.Name = "urbex"
	}
	if cfg.Cloudflare.Hostnames == "" {
		cfg.Cloudflare.Hostnames = HostnamesFlat
	}
	if cfg.Cloudflare.TunnelName == "" {
		cfg.Cloudflare.TunnelName = cfg.Cloudflare.Name + "-platform"
	}
	if cfg.Keycloak.PlatformRealm == "" {
		cfg.Keycloak.PlatformRealm = "platform"
	}
}

// Validate returns a human-readable list of problems with the config, or
// an empty slice if it's valid enough to attempt a bootstrap run.
func (c *Config) Validate() []string {
	var errs []string

	require := func(v, field string) {
		if v == "" {
			errs = append(errs, field+" is required")
		}
	}

	require(c.Domain, "domain")
	require(c.Proxmox.APIURL, "proxmox.apiUrl")
	require(c.Proxmox.Node, "proxmox.node")
	require(c.Proxmox.LXCTemplate, "proxmox.lxcTemplate")
	require(c.Proxmox.SSHPublicKey, "proxmox.sshPublicKey")
	require(c.Proxmox.Network.CIDR, "proxmox.network.cidr")
	require(c.Proxmox.Network.Gateway, "proxmox.network.gateway")
	require(c.Git.GiteaURL, "git.giteaUrl")

	cf := c.Cloudflare
	if (cf.AccountID == "") != (cf.ZoneID == "") {
		errs = append(errs, "cloudflare.accountId and cloudflare.zoneId go together: set both, or neither")
	}
	if cf.Hostnames != "" && cf.Hostnames != HostnamesFlat && cf.Hostnames != HostnamesNested {
		errs = append(errs, "cloudflare.hostnames must be flat or nested")
	}
	if cf.Enabled() && len(cf.Access.Emails) == 0 {
		errs = append(errs, "cloudflare.access.emails is required: who may reach Gitea and Komodo through Cloudflare Access")
	}
	for _, f := range c.DNS.Forwarders {
		if net.ParseIP(f) == nil {
			errs = append(errs, "dns.forwarders: "+f+" is not an IP address")
		}
	}
	if z := c.DNS.Zone; z != "" && (strings.Contains(z, "..") || strings.HasPrefix(z, ".") || !strings.Contains(z, ".")) {
		errs = append(errs, "dns.zone: "+z+" is not a domain name")
	}
	if cf.QuickTunnel && cf.Enabled() {
		errs = append(errs, "cloudflare.quickTunnel and cloudflare.accountId/zoneId are alternatives: set one, not both")
	}
	for _, name := range slices.Sorted(maps.Keys(c.Storage.Shares)) {
		if p := c.Storage.Shares[name].Path; !strings.HasPrefix(p, "/") || p == "/" || strings.ContainsAny(p, ", \t") {
			errs = append(errs, "storage.shares."+name+".path: "+p+" is not a folder on the Proxmox node")
		}
	}

	return errs
}
