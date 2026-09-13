// Package credentials loads sensitive values that must never be written
// to the GitOps repo. See docs/decisions/0012-platform-config-and-credentials.md
// in urbforge/urbex: environment variables take precedence, falling back
// to ~/.urbex/credentials.yaml.
package credentials

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Credentials holds secrets read from the environment or from
// ~/.urbex/credentials.yaml. Never log or serialize this back out.
type Credentials struct {
	ProxmoxToken    string `yaml:"proxmoxToken"`
	CloudflareToken string `yaml:"cloudflareToken"`
	GiteaToken      string `yaml:"giteaToken"`
	AgeKey          string `yaml:"ageKey"`
}

// Load reads credentials from environment variables first
// (URBEX_PROXMOX_TOKEN, URBEX_CLOUDFLARE_TOKEN, URBEX_GITEA_TOKEN,
// URBEX_AGE_KEY), then fills in anything still missing from
// ~/.urbex/credentials.yaml if that file exists.
func Load() (*Credentials, error) {
	c := &Credentials{
		ProxmoxToken:    os.Getenv("URBEX_PROXMOX_TOKEN"),
		CloudflareToken: os.Getenv("URBEX_CLOUDFLARE_TOKEN"),
		GiteaToken:      os.Getenv("URBEX_GITEA_TOKEN"),
		AgeKey:          os.Getenv("URBEX_AGE_KEY"),
	}

	if c.complete() {
		return c, nil
	}

	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}

	fromFile, err := loadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}

	c.fillFrom(fromFile)
	return c, nil
}

// DefaultPath returns ~/.urbex/credentials.yaml for the current user.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, ".urbex", "credentials.yaml"), nil
}

func loadFile(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var c Credentials
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &c, nil
}

func (c *Credentials) complete() bool {
	return c.ProxmoxToken != "" && c.CloudflareToken != "" && c.GiteaToken != "" && c.AgeKey != ""
}

func (c *Credentials) fillFrom(other *Credentials) {
	if c.ProxmoxToken == "" {
		c.ProxmoxToken = other.ProxmoxToken
	}
	if c.CloudflareToken == "" {
		c.CloudflareToken = other.CloudflareToken
	}
	if c.GiteaToken == "" {
		c.GiteaToken = other.GiteaToken
	}
	if c.AgeKey == "" {
		c.AgeKey = other.AgeKey
	}
}

// MissingForProvisioning reports which credentials required for the
// Terraform+Ansible provisioning phase of urbex bootstrap are missing.
// Cloudflare/Gitea tokens are needed only by later phases (tunnel setup,
// pushing to Gitea) and are checked separately once those are
// implemented.
func (c *Credentials) MissingForProvisioning() []string {
	var missing []string
	if c.ProxmoxToken == "" {
		missing = append(missing, "URBEX_PROXMOX_TOKEN (or proxmoxToken in ~/.urbex/credentials.yaml)")
	}
	if c.AgeKey == "" {
		missing = append(missing, "URBEX_AGE_KEY (or ageKey in ~/.urbex/credentials.yaml)")
	}
	return missing
}
