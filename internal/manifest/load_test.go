package manifest

import (
	"os"
	"testing"
)

func TestLoadTyped_Valid(t *testing.T) {
	m, result, err := LoadTyped("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected no validation errors, got: %v", result.Errors)
	}
	if m == nil {
		t.Fatal("expected a non-nil Manifest for a valid file")
	}

	if m.Project != "my-app" {
		t.Errorf("Project = %q, want my-app", m.Project)
	}
	if len(m.Services) != 1 || m.Services[0].Name != "api" {
		t.Fatalf("unexpected services: %+v", m.Services)
	}
	if m.Services[0].Runtime != "go" || m.Services[0].Port != 8080 {
		t.Errorf("unexpected service fields: %+v", m.Services[0])
	}
	if m.Email == nil || m.Email.Provider != "brevo" {
		t.Errorf("expected email.provider brevo, got: %+v", m.Email)
	}
}

func TestLoadTyped_Invalid(t *testing.T) {
	m, result, err := LoadTyped("testdata/invalid.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatal("expected a nil Manifest for an invalid file")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected validation errors")
	}
}

func TestService_ResourcesOrDefault(t *testing.T) {
	bare := Service{}
	got := bare.ResourcesOrDefault()
	if got.CPU != DefaultCPU || got.Memory != DefaultMemory || got.Disk != DefaultDisk {
		t.Errorf("expected all defaults, got %+v", got)
	}

	partial := Service{Resources: &Resources{CPU: 4}}
	got = partial.ResourcesOrDefault()
	if got.CPU != 4 {
		t.Errorf("expected overridden CPU 4, got %d", got.CPU)
	}
	if got.Memory != DefaultMemory {
		t.Errorf("expected default memory, got %q", got.Memory)
	}
}

func TestService_EnvFor(t *testing.T) {
	s := Service{Env: map[string]map[string]string{
		"staging": {"LOG_LEVEL": "debug"},
	}}
	if got := s.EnvFor("staging"); got["LOG_LEVEL"] != "debug" {
		t.Errorf("EnvFor(staging) = %v", got)
	}
	if got := s.EnvFor("prod"); got != nil {
		t.Errorf("EnvFor(prod) = %v, want nil", got)
	}
}

// devices are files under /dev, each once; the typed manifest has them.
func TestLoadTyped_Devices(t *testing.T) {
	write := func(devices string) string {
		path := t.TempDir() + "/urbex.yaml"
		body := "apiVersion: urbex/v1\nproject: media\nservices:\n  - name: gluetun\n    image: qmcgaw/gluetun\n    version: \"3\"\n    port: 8000\n    devices: " + devices + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	m, result, err := LoadTyped(write("[/dev/net/tun]"))
	if err != nil || m == nil {
		t.Fatalf("expected a valid manifest, got %v %v", err, result.Errors)
	}
	if got := m.Services[0].Devices; len(got) != 1 || got[0] != "/dev/net/tun" {
		t.Errorf("Devices = %v", got)
	}
	for _, bad := range []string{"[net/tun]", "[/dev/net/tun, /dev/net/tun]", "[\"/dev/net/tun,mode=0666\"]"} {
		if m, _, err := LoadTyped(write(bad)); err != nil || m != nil {
			t.Errorf("devices %s: expected refused by the schema, got %v %v", bad, m, err)
		}
	}
}
