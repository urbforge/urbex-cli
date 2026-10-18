package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

const savedManifest = `apiVersion: urbex/v1
project: demo
services:
  - name: web
    image: docker.io/library/nginx
    version: "1.29"
    port: 80
`

func savedManifestFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "urbex.yaml")
	if err := os.WriteFile(path, []byte(savedManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func imageManifest(envs ...string) *manifest.Manifest {
	return &manifest.Manifest{
		Project:      "demo",
		Environments: envs,
		Services: []manifest.Service{
			{Name: "web", Image: "docker.io/library/nginx", Version: "1.29", Port: 80},
		},
	}
}

func TestSaveVersion_FollowsTheFirstEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		envs    []string
		env     string
		written bool
		said    string
	}{
		{"staging of a stack with both", nil, "staging", true, "urbex.yaml: web version 1.29 -> 1.28 (not committed)"},
		{"prod of a stack with both", nil, "prod", false, "urbex.yaml follows staging"},
		{"prod of a stack with only prod", []string{"prod"}, "prod", true, "urbex.yaml: web version 1.29 -> 1.28 (not committed)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := savedManifestFile(t)
			var out strings.Builder
			if err := saveVersion(&out, imageManifest(tc.envs...), path, tc.env, "web", "1.28"); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			want := savedManifest
			if tc.written {
				want = strings.Replace(savedManifest, `"1.29"`, `"1.28"`, 1)
			}
			if string(data) != want {
				t.Errorf("manifest after saveVersion:\n%s", data)
			}
			if !strings.Contains(out.String(), tc.said) {
				t.Errorf("expected %q in the output, got %q", tc.said, out.String())
			}
		})
	}
}

func TestSaveVersion_SaysNothingWhenTheTagIsAlreadyThere(t *testing.T) {
	path := savedManifestFile(t)
	var out strings.Builder
	if err := saveVersion(&out, imageManifest("prod"), path, "prod", "web", "1.29"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
	if data, _ := os.ReadFile(path); string(data) != savedManifest {
		t.Errorf("the manifest should be untouched:\n%s", data)
	}
}

func TestSaveVersion_ReportsAManifestItCannotEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urbex.yaml")
	inline := strings.Replace(savedManifest, `version: "1.29"`, `version: >-
      1.29`, 1)
	if err := os.WriteFile(path, []byte(inline), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := saveVersion(&out, imageManifest("prod"), path, "prod", "web", "1.28")
	if err == nil || !strings.Contains(err.Error(), "deployed, but urbex.yaml was not updated") {
		t.Fatalf("expected the deploy to be reported as done and the manifest as not updated, got %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != inline {
		t.Errorf("a manifest it can't edit must be left alone:\n%s", data)
	}
}
