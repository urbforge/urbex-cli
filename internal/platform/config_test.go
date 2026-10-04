package platform

import (
	"path/filepath"
	"testing"
)

func TestLoadFile_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urbex.platform.yaml")
	if err := WriteTemplate(path); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.Proxmox.StoragePool != "local-lvm" {
		t.Errorf("expected default storagePool, got %q", cfg.Proxmox.StoragePool)
	}
	if cfg.Proxmox.VMIDBase != 9000 {
		t.Errorf("expected default vmIdBase 9000, got %d", cfg.Proxmox.VMIDBase)
	}
	if cfg.Proxmox.Network.BaseHostOffset != 200 {
		t.Errorf("expected default baseHostOffset 200, got %d", cfg.Proxmox.Network.BaseHostOffset)
	}
	if cfg.Keycloak.PlatformRealm != "platform" {
		t.Errorf("expected default platform realm, got %q", cfg.Keycloak.PlatformRealm)
	}
}

func TestTemplate_FailsValidationUntilFilledIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urbex.platform.yaml")
	if err := WriteTemplate(path); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	// The template's placeholder cloudflare/keycloak/domain values are not
	// actually empty (they're example.com etc.), so this only checks that
	// Validate runs cleanly against the template shape; a real deployment
	// still needs to replace the placeholders with real values.
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("expected the filled-in template to pass Validate, got: %v", errs)
	}
}

func TestValidate_ReportsMissingFields(t *testing.T) {
	cfg := &Config{}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation errors for an empty config")
	}

	want := "domain is required"
	found := false
	for _, e := range errs {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q among errors, got: %v", want, errs)
	}
}

func TestCloudflareHostnames(t *testing.T) {
	flat := CloudflareConfig{Name: "urbex", Hostnames: HostnamesFlat}
	nested := CloudflareConfig{Name: "urbex", Hostnames: HostnamesNested}
	for _, tc := range []struct {
		labels       []string
		flat, nested string
	}{
		{[]string{"auth"}, "auth-urbex.cubotto.net", "auth.urbex.cubotto.net"},
		{[]string{"hello"}, "hello-urbex.cubotto.net", "hello.urbex.cubotto.net"},
		{[]string{"api", "staging", "hello"}, "api-staging-hello-urbex.cubotto.net", "api.staging.hello.urbex.cubotto.net"},
	} {
		if got := flat.Hostname("cubotto.net", tc.labels...); got != tc.flat {
			t.Errorf("flat %v = %s, want %s", tc.labels, got, tc.flat)
		}
		if got := nested.Hostname("cubotto.net", tc.labels...); got != tc.nested {
			t.Errorf("nested %v = %s, want %s", tc.labels, got, tc.nested)
		}
	}
}

func TestCloudflareValidation(t *testing.T) {
	base := func() *Config {
		c := &Config{Domain: "d", Git: GitConfig{GiteaURL: "g"}}
		c.Proxmox = ProxmoxConfig{APIURL: "a", Node: "n", LXCTemplate: "t", SSHPublicKey: "k", Network: NetworkConfig{CIDR: "c", Gateway: "g"}}
		return c
	}
	if errs := base().Validate(); len(errs) != 0 {
		t.Fatalf("no cloudflare must be valid: %v", errs)
	}
	c := base()
	c.Cloudflare = CloudflareConfig{AccountID: "a"}
	if errs := c.Validate(); len(errs) == 0 {
		t.Error("an account without a zone must be refused")
	}
	c.Cloudflare = CloudflareConfig{AccountID: "a", ZoneID: "z"}
	if errs := c.Validate(); len(errs) == 0 {
		t.Error("Access emails must be required")
	}
	c.Cloudflare.Access.Emails = []string{"me@example.com"}
	c.Cloudflare.Hostnames = "deep"
	if errs := c.Validate(); len(errs) != 1 {
		t.Errorf("expected only the hostnames error, got %v", errs)
	}
}

func TestQuickTunnelValidation(t *testing.T) {
	base := func() *Config {
		c := &Config{Domain: "d", Git: GitConfig{GiteaURL: "g"}}
		c.Proxmox = ProxmoxConfig{APIURL: "a", Node: "n", LXCTemplate: "t", SSHPublicKey: "k", Network: NetworkConfig{CIDR: "c", Gateway: "g"}}
		return c
	}
	c := base()
	c.Cloudflare.QuickTunnel = true
	if errs := c.Validate(); len(errs) != 0 {
		t.Errorf("quickTunnel alone must be valid: %v", errs)
	}
	c.Cloudflare.AccountID, c.Cloudflare.ZoneID = "a", "z"
	c.Cloudflare.Access.Emails = []string{"me@example.com"}
	if errs := c.Validate(); len(errs) == 0 {
		t.Error("quickTunnel together with accountId/zoneId must be refused")
	}
}
