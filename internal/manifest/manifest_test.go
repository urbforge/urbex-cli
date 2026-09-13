package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFile_Valid(t *testing.T) {
	result, err := ValidateFile("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got: %v", result.Errors)
	}
}

func TestValidateFile_InvalidEnum(t *testing.T) {
	result, err := ValidateFile("testdata/invalid.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected validation errors, got none")
	}
}

func TestValidateFile_RequiresFrontendOrServices(t *testing.T) {
	path := writeTemp(t, `
apiVersion: urbex/v1
project: my-app
`)

	result, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected an error for a manifest with neither frontend nor services")
	}
}

func TestValidateFile_DockerRuntimeRequiresDockerfile(t *testing.T) {
	path := writeTemp(t, `
apiVersion: urbex/v1
project: my-app
services:
  - name: api
    runtime: docker
    port: 8080
`)

	result, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected an error for runtime: docker without a dockerfile")
	}
}

func TestValidateFile_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "not: [valid: yaml")

	result, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestWriteTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urbex.yaml")

	if err := WriteTemplate(path, "my-app"); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}

	result, err := ValidateFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected the bare template (no frontend/services) to fail validation")
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "urbex.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp manifest: %v", err)
	}
	return path
}
