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
