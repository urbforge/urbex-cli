// Package platform loads, validates, and scaffolds urbex.platform.yaml,
// the platform-wide (non-sensitive) configuration used by urbex bootstrap.
// See docs/decisions/0012-platform-config-and-credentials.md in
// urbforge/urbex.
package platform

import (
	"fmt"
	"os"

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
}

type GitConfig struct {
	GiteaURL string `yaml:"giteaUrl"`
	Org      string `yaml:"org"`
}

type CloudflareConfig struct {
	AccountID  string `yaml:"accountId"`
	ZoneID     string `yaml:"zoneId"`
	TunnelName string `yaml:"tunnelName"`
}

type ProxmoxConfig struct {
	APIURL       string        `yaml:"apiUrl"`
	Insecure     bool          `yaml:"insecure"`
	Node         string        `yaml:"node"`
	StoragePool  string        `yaml:"storagePool"`
	LXCTemplate  string        `yaml:"lxcTemplate"`
	SSHPublicKey string        `yaml:"sshPublicKey"`
	VMIDBase     int           `yaml:"vmIdBase"`
	Network      NetworkConfig `yaml:"network"`
}

// NetworkConfig describes the network base-service LXCs are attached to.
// Base services get static IPs deterministically allocated from CIDR,
// starting at BaseHostOffset - see internal/netalloc.
type NetworkConfig struct {
	CIDR           string `yaml:"cidr"`
	Gateway        string `yaml:"gateway"`
	BaseHostOffset int    `yaml:"baseHostOffset"`
}

type DNSConfig struct {
	TechnitiumURL string `yaml:"technitiumUrl"`
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

	return errs
}
