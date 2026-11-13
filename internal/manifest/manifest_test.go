package manifest

import (
	"os"
	"path/filepath"
	"strings"
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

func TestShipsLogs_UnlessSetToFalse(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		obs  *Observability
		want bool
	}{
		{nil, true},
		{&Observability{Metrics: true}, true},
		{&Observability{Logs: &yes}, true},
		{&Observability{Logs: &no}, false},
	} {
		if got := (&Manifest{Observability: tc.obs}).ShipsLogs(); got != tc.want {
			t.Errorf("ShipsLogs(%+v) = %v, want %v", tc.obs, got, tc.want)
		}
	}
}

func TestValidateFile_ApplicationsFromImages(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		valid      bool
	}{
		{"image and version", `
apiVersion: urbex/v1
project: my-app
services:
  - name: cache
    image: valkey/valkey
    version: "9"
`, true},
		{"image with a port", `
apiVersion: urbex/v1
project: my-app
environments: [prod]
services:
  - name: vault
    image: vaultwarden/server
    version: "1.34.3"
    port: 80
    public: true
`, true},
		{"image without version", `
apiVersion: urbex/v1
project: my-app
services:
  - name: cache
    image: valkey/valkey
`, false},
		{"image and runtime", `
apiVersion: urbex/v1
project: my-app
services:
  - name: cache
    image: valkey/valkey
    version: "9"
    runtime: go
    port: 8080
`, false},
		{"runtime without port", `
apiVersion: urbex/v1
project: my-app
services:
  - name: api
    runtime: go
`, false},
		{"staging only", `
apiVersion: urbex/v1
project: my-app
environments: [staging]
services:
  - name: api
    runtime: go
    port: 8080
`, false},
		{"unknown environment", `
apiVersion: urbex/v1
project: my-app
environments: [prod, qa]
services:
  - name: api
    runtime: go
    port: 8080
`, false},
	} {
		result, err := ValidateFile(writeTemp(t, tc.yaml))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if valid := len(result.Errors) == 0; valid != tc.valid {
			t.Errorf("%s: valid = %v, want %v (%v)", tc.name, valid, tc.valid, result.Errors)
		}
	}
}

func TestValidateFile_Volumes(t *testing.T) {
	const head = "apiVersion: urbex/v1\nproject: media\nenvironments: [prod]\nservices:\n  - name: jellyfin\n    image: jellyfin/jellyfin\n    version: \"10\"\n    port: 8096\n    volumes:\n"
	for _, tc := range []struct {
		name, volumes string
		valid         bool
	}{
		{"named volume and share", "      - { name: config, path: /config, size: 5Gi }\n      - { share: media, path: /media, readOnly: true }\n", true},
		{"named volume, default size", "      - { name: config, path: /config }\n", true},
		{"name and share", "      - { name: config, share: media, path: /config }\n", false},
		{"share with a size", "      - { share: media, path: /media, size: 5Gi }\n", false},
		{"named volume read only", "      - { name: config, path: /config, readOnly: true }\n", false},
		{"neither name nor share", "      - { path: /config }\n", false},
		{"mounted at /", "      - { name: config, path: / }\n", false},
	} {
		result, err := ValidateFile(writeTemp(t, head+tc.volumes))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if valid := len(result.Errors) == 0; valid != tc.valid {
			t.Errorf("%s: valid = %v, want %v (%v)", tc.name, valid, tc.valid, result.Errors)
		}
	}
}

func TestLoadTyped_ApplicationFromAnImage(t *testing.T) {
	m, result, err := LoadTyped(writeTemp(t, `
apiVersion: urbex/v1
project: photos
environments: [prod]
services:
  - name: api
    runtime: go
    port: 8080
  - name: cache
    image: valkey/valkey
    version: "9"
`))
	if err != nil || m == nil {
		t.Fatalf("LoadTyped: %v %v", err, result)
	}
	if built := Built(m.Deployables()); len(built) != 1 || built[0].Name != "api" {
		t.Errorf("Built = %+v", built)
	}
	if images := FromImages(m.Deployables()); len(images) != 1 || images[0].Image != "valkey/valkey" || images[0].Version != "9" {
		t.Errorf("FromImages = %+v", images)
	}
	if !m.SingleEnvironment() || m.HasEnvironment("staging") || !m.HasEnvironment("prod") {
		t.Errorf("expected a prod-only stack, got %v", m.EnvironmentList())
	}
	if got := (&Manifest{}).EnvironmentList(); len(got) != 2 || got[0] != "staging" || got[1] != "prod" {
		t.Errorf("default environments = %v", got)
	}
}

func TestUnsupported_RefusesWhatIsNotActedOnYet(t *testing.T) {
	m := &Manifest{Services: []Service{
		{Name: "db", Image: "postgres", Version: "17", Provides: "postgres", Volumes: []Volume{{Name: "data", Path: "/var/lib/postgresql"}}},
		{Name: "api", Runtime: "go", Port: 8080, Uses: []string{"db"}},
		{Name: "ui", Image: "nginx", Version: "1", Public: true, Healthcheck: "/"},
		{Name: "ok", Image: "nginx", Version: "1", Port: 80, Public: true},
		{Name: "media", Image: "jellyfin/jellyfin", Version: "10", Port: 8096, Volumes: []Volume{
			{Name: "config", Path: "/config"},
			{Share: "media", Path: "/media"},
			{Share: "media", Path: "/config"},
		}},
	}}
	got := m.Unsupported()
	want := []string{
		"services ui: public needs a port",
		"services ui: healthcheck needs a port",
		"services media: share media is declared twice",
		"services media: two volumes are mounted at /config",
	}
	if len(got) != len(want) {
		t.Fatalf("Unsupported = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Unsupported[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// An application in a VM gets at least 1Gi and an 8Gi disk by default,
// and none of what a VM doesn't have yet (ADR-0033).
func TestMachine_VM(t *testing.T) {
	lxc := Service{Name: "api"}
	vm := Service{Name: "backup", Machine: MachineVM}
	if lxc.IsVM() || !vm.IsVM() || (Service{Machine: MachineLXC}).IsVM() {
		t.Error("only machine: vm runs in a VM")
	}
	if got := lxc.ResourcesOrDefault().Disk; got != "5Gi" {
		t.Errorf("LXC disk = %s, want 5Gi", got)
	}
	if got := vm.ResourcesOrDefault(); got.Disk != "8Gi" || got.Memory != "1Gi" {
		t.Errorf("VM resources = %+v, want 8Gi disk, 1Gi memory", got)
	}
	vm.Resources = &Resources{Disk: "20Gi"}
	if got := vm.ResourcesOrDefault().Disk; got != "20Gi" {
		t.Errorf("VM disk = %s, want the declared 20Gi", got)
	}

	m := &Manifest{Services: []Service{
		{Name: "small", Machine: MachineVM, Resources: &Resources{Memory: "512Mi", Disk: "4Gi"}},
		{Name: "big", Machine: MachineVM, Resources: &Resources{Memory: "2Gi"}},
		{Name: "lxc", Resources: &Resources{Memory: "512Mi"}, Volumes: []Volume{{Name: "data", Path: "/data"}, {Share: "media", Path: "/media"}}},
		{Name: "kept", Machine: MachineVM, Volumes: []Volume{{Name: "data", Path: "/data"}, {Share: "media", Path: "/media"}}},
	}}
	got := m.Unsupported()
	want := []string{
		"services small: a VM needs at least 1Gi of memory, not 512Mi",
		"services small: a VM needs a disk of at least 5Gi, not 4Gi",
		"services kept: shares are not available in a VM yet (urbforge/urbex-cli#83): use machine: lxc",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Unsupported =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The schema takes machine: lxc or vm, and nothing else.
func TestValidateFile_Machine(t *testing.T) {
	for machine, valid := range map[string]bool{"lxc": true, "vm": true, "container": false} {
		path := filepath.Join(t.TempDir(), "urbex.yaml")
		os.WriteFile(path, []byte("apiVersion: urbex/v1\nproject: hello\nservices:\n  - name: backup\n    image: rclone/rclone\n    version: \"1.71\"\n    machine: "+machine+"\n"), 0o644)
		res, err := ValidateFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if (len(res.Errors) == 0) != valid {
			t.Errorf("machine: %s valid = %v, want %v (%v)", machine, len(res.Errors) == 0, valid, res.Errors)
		}
	}
}

// A named volume's disk serial is <application>-<name>, cut to Proxmox's
// 20 characters with a hash when it is longer.
func TestDiskSerial(t *testing.T) {
	svc := Service{Name: "web"}
	if got := svc.DiskSerial(Volume{Name: "data"}); got != "web-data" {
		t.Errorf("serial = %s", got)
	}
	long := Service{Name: "photoprism"}
	a, b := long.DiskSerial(Volume{Name: "originals-2026"}), long.DiskSerial(Volume{Name: "originals-2027"})
	if len(a) != MaxDiskSerial || !strings.HasPrefix(a, "photoprism-") || a == b {
		t.Errorf("long serials = %s, %s", a, b)
	}
	if a != long.DiskSerial(Volume{Name: "originals-2026"}) {
		t.Error("the same volume must keep its serial")
	}
}
