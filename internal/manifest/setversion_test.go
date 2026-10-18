package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const versionManifest = `# A comment that stays.
apiVersion: urbex/v1
project: demo
services:
  - name: web
    image: docker.io/library/nginx
    version: "1.29" # the tag
    port: 80
  - name: db
    image: docker.io/library/postgres
    version: 17.6
  - name: cache
    image: docker.io/library/redis
    version: '7.2'
  - name: plain
    image: docker.io/library/alpine
    version: latest
  - name: inline
    image: docker.io/library/busybox
    meta: {version: 1, x: y}
`

func writeVersionManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "urbex.yaml")
	if err := os.WriteFile(path, []byte(versionManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetServiceVersion(t *testing.T) {
	for _, tc := range []struct {
		name, service, version string
		want                   string // the line that now holds the version
	}{
		{"double quotes kept, comment kept", "web", "1.28", `    version: "1.28" # the tag`},
		{"plain number gets quotes", "db", "17.7", `    version: "17.7"`},
		{"single quotes kept", "cache", "7.4", `    version: '7.4'`},
		{"plain stays plain when it is still a string", "db", "17.7-alpine", `    version: 17.7-alpine`},
		{"quoted when plain would be a number", "plain", "1.30", `    version: "1.30"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeVersionManifest(t)
			changed, err := SetServiceVersion(path, tc.service, tc.version)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			data, _ := os.ReadFile(path)
			want := strings.Replace(versionManifest, versionLineOf(tc.service), tc.want, 1)
			if string(data) != want {
				t.Errorf("only that line should change:\n%s", data)
			}
		})
	}
}

func versionLineOf(service string) string {
	return map[string]string{
		"web":   `    version: "1.29" # the tag`,
		"db":    `    version: 17.6`,
		"cache": `    version: '7.2'`,
		"plain": `    version: latest`,
	}[service]
}

func TestSetServiceVersion_ManifestStaysValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urbex.yaml")
	manifest := "apiVersion: urbex/v1\nproject: demo\nservices:\n  - name: web\n    image: docker.io/library/nginx\n    version: \"1.29\"\n    port: 80\n"
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetServiceVersion(path, "web", "1.28"); err != nil {
		t.Fatal(err)
	}
	m, res, err := LoadTyped(path)
	if err != nil || m == nil {
		t.Fatalf("the edited manifest should still be valid: %v %v", err, res)
	}
	if got := m.Services[0].Version; got != "1.28" {
		t.Errorf("version = %q", got)
	}
}

func TestSetServiceVersion_SameVersionChangesNothing(t *testing.T) {
	path := writeVersionManifest(t)
	changed, err := SetServiceVersion(path, "web", "1.29")
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != versionManifest {
		t.Error("the file should be untouched")
	}
}

func TestSetServiceVersion_Refuses(t *testing.T) {
	path := writeVersionManifest(t)
	for _, service := range []string{"missing", "inline"} {
		if _, err := SetServiceVersion(path, service, "2"); err == nil {
			t.Errorf("%s: expected an error", service)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != versionManifest {
		t.Error("a refused change must leave the file as it was")
	}
}

func TestLeadingEnvironment(t *testing.T) {
	for _, tc := range []struct {
		envs []string
		want string
	}{
		{nil, "staging"},
		{[]string{"staging", "prod"}, "staging"},
		{[]string{"prod"}, "prod"},
	} {
		if got := (&Manifest{Environments: tc.envs}).LeadingEnvironment(); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.envs, got, tc.want)
		}
	}
}
