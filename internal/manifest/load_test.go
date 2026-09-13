package manifest

import "testing"

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
