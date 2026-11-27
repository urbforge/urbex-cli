package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"URBEX_PROXMOX_TOKEN",
		"URBEX_CLOUDFLARE_TOKEN",
		"URBEX_GITEA_TOKEN",
		"URBEX_FORGE_TOKEN",
		"URBEX_GITEA_ADMIN_PASSWORD",
		"URBEX_FORGE_ADMIN_PASSWORD",
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

func TestUpdate_PersistsOnlyChangedValuesWithPrivateMode(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".urbex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "credentials.yaml")
	if err := os.WriteFile(path, []byte("ageKey: file-age-key\n"), 0o644); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	t.Setenv("URBEX_PROXMOX_TOKEN", "env-pve-token")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Update(func(c *Credentials) { c.ForgeToken = "generated" }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if c.ForgeToken != "generated" {
		t.Errorf("in-memory ForgeToken = %q, want generated", c.ForgeToken)
	}

	onDisk, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if onDisk.ForgeToken != "generated" || onDisk.AgeKey != "file-age-key" {
		t.Errorf("unexpected file content: %+v", onDisk)
	}
	if onDisk.ProxmoxToken != "" {
		t.Errorf("env-sourced ProxmoxToken must not be copied into the file, got %q", onDisk.ProxmoxToken)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestEnv_ReturnsOnlyRequestedVariables(t *testing.T) {
	c := &Credentials{ForgeAdminPassword: "pw", KomodoDBPassword: "db", ProxmoxToken: "secret"}
	got := c.Env("URBEX_FORGE_ADMIN_PASSWORD", "URBEX_KOMODO_DB_PASSWORD")
	want := []string{"URBEX_FORGE_ADMIN_PASSWORD=pw", "URBEX_KOMODO_DB_PASSWORD=db"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Env = %v, want %v", got, want)
	}
}

func TestClearGenerated_KeepsWhatTheUserProvides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &Credentials{}
	if err := c.Update(func(c *Credentials) {
		c.ProxmoxToken, c.AgeKey, c.CloudflareToken = "pve", "age", "cf"
		c.ForgeToken, c.KomodoAPIKey, c.GrafanaAdminPassword = "g", "k", "p"
		c.BackupPassword, c.BackupS3SecretAccessKey = "restic", "s3"
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.ClearGenerated(); err != nil {
		t.Fatal(err)
	}
	path, _ := DefaultPath()
	onDisk, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []*Credentials{c, onDisk} {
		if got.ProxmoxToken != "pve" || got.AgeKey != "age" || got.CloudflareToken != "cf" {
			t.Errorf("provided credentials lost: %+v", got)
		}
		// Without the backup password the backups can't be read: it
		// outlives the platform (urbforge/urbex-cli#122).
		if got.BackupPassword != "restic" || got.BackupS3SecretAccessKey != "s3" {
			t.Errorf("the backups' credentials lost: %+v", got)
		}
		if got.ForgeToken != "" || got.KomodoAPIKey != "" || got.GrafanaAdminPassword != "" {
			t.Errorf("generated credentials kept: %+v", got)
		}
	}
}

// A workspace written before the forge was Forgejo (ADR-0034) works: the
// legacy names are read, from the file and the environment, the new ones
// win, and the next write moves the file to the new keys.
func TestLoad_LegacyForgeNames(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".urbex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.yaml")
	if err := os.WriteFile(path, []byte("giteaToken: old-token\ngiteaAdminPassword: old-pw\nageKey: k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ForgeToken != "old-token" || c.ForgeAdminPassword != "old-pw" {
		t.Fatalf("legacy keys not read: %+v", c)
	}
	if c.Get("URBEX_GITEA_TOKEN") != "old-token" || c.Get("URBEX_FORGE_TOKEN") != "old-token" {
		t.Error("Get should answer to both names")
	}

	if err := c.Update(func(x *Credentials) { x.KomodoAPIKey = "k2" }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if got := string(data); !strings.Contains(got, "forgeToken: old-token") || strings.Contains(got, "giteaToken") {
		t.Errorf("the file should hold the new keys only, got:\n%s", got)
	}

	t.Setenv("URBEX_GITEA_TOKEN", "env-old")
	if c, _ := Load(); c.ForgeToken != "env-old" {
		t.Errorf("legacy env not read: %q", c.ForgeToken)
	}
	t.Setenv("URBEX_FORGE_TOKEN", "env-new")
	if c, _ := Load(); c.ForgeToken != "env-new" {
		t.Errorf("the new env name should win: %q", c.ForgeToken)
	}
}

// The backups' copy keeps every credential but the main Proxmox and
// Cloudflare tokens (urbforge/urbex-cli#122).
func TestBackupCopy_LeavesOutTheMainTokens(t *testing.T) {
	c := &Credentials{ProxmoxToken: "pve-main", CloudflareToken: "cf-main", ProxmoxBackupToken: "pve-backup",
		AgeKey: "AGE-SECRET-KEY-1X", BackupPassword: "pw", ForgeAdminPassword: "forge-pw"}
	data, err := c.BackupCopy()
	if err != nil {
		t.Fatal(err)
	}
	var back Credentials
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.ProxmoxToken != "" || back.CloudflareToken != "" {
		t.Errorf("main tokens in the copy: %q", data)
	}
	if back.ProxmoxBackupToken != "pve-backup" || back.BackupPassword != "pw" || back.ForgeAdminPassword != "forge-pw" {
		t.Errorf("copy lost credentials: %q", data)
	}
	if c.ProxmoxToken != "pve-main" {
		t.Error("BackupCopy changed the credentials")
	}
}
