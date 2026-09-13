package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"URBEX_PROXMOX_TOKEN",
		"URBEX_CLOUDFLARE_TOKEN",
		"URBEX_GITEA_TOKEN",
		"URBEX_AGE_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_FromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("HOME", t.TempDir()) // no credentials file present
	t.Setenv("URBEX_PROXMOX_TOKEN", "pve-token")
	t.Setenv("URBEX_AGE_KEY", "age-key")
	t.Setenv("URBEX_CLOUDFLARE_TOKEN", "cf-token")
	t.Setenv("URBEX_GITEA_TOKEN", "gitea-token")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProxmoxToken != "pve-token" || c.AgeKey != "age-key" {
		t.Errorf("unexpected credentials: %+v", c)
	}
	if len(c.MissingForProvisioning()) != 0 {
		t.Errorf("expected no missing provisioning credentials, got: %v", c.MissingForProvisioning())
	}
}

func TestLoad_FallsBackToFile(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".urbex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "proxmoxToken: file-pve-token\nageKey: file-age-key\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProxmoxToken != "file-pve-token" {
		t.Errorf("ProxmoxToken = %q, want file-pve-token", c.ProxmoxToken)
	}
	if c.AgeKey != "file-age-key" {
		t.Errorf("AgeKey = %q, want file-age-key", c.AgeKey)
	}
}

func TestLoad_MissingForProvisioning(t *testing.T) {
	clearEnv(t)
	t.Setenv("HOME", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	missing := c.MissingForProvisioning()
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing provisioning credentials, got: %v", missing)
	}
}

func TestLoad_EnvTakesPrecedenceOverFile(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".urbex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "proxmoxToken: file-pve-token\nageKey: file-age-key\ncloudflareToken: file-cf\ngiteaToken: file-gitea\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}

	t.Setenv("URBEX_PROXMOX_TOKEN", "env-pve-token")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProxmoxToken != "env-pve-token" {
		t.Errorf("ProxmoxToken = %q, want env-pve-token (env should win)", c.ProxmoxToken)
	}
	if c.AgeKey != "file-age-key" {
		t.Errorf("AgeKey = %q, want file-age-key (fallback to file)", c.AgeKey)
	}
}
