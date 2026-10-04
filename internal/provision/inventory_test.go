package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

func TestWriteInventory_PublicServiceGroup(t *testing.T) {
	repoPath := t.TempDir()
	ansibleDir := filepath.Join(repoPath, "ansible", "projects", "my-app", "staging")
	services := []manifest.Service{
		{Name: "api", Runtime: "go", Port: 8080, Public: true},
		{Name: "worker", Runtime: "go", Port: 9090},
	}
	allocations := map[string]allocation.Entry{
		Hostname("my-app", "api", "staging"):    {IP: "192.168.1.210", VMID: 9100},
		Hostname("my-app", "worker", "staging"): {IP: "192.168.1.211", VMID: 9101},
	}

	if err := writeInventory(repoPath, ansibleDir, "my-app", "staging", services, allocations, bootstrap.Endpoints{}, true, true); err != nil {
		t.Fatalf("writeInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ansibleDir, "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := string(data)

	if !strings.Contains(inventory, "public_service:") {
		t.Errorf("expected a public_service group:\n%s", inventory)
	}
	if !strings.Contains(inventory, "my-app-api-staging") {
		t.Errorf("expected the public service's host in the inventory:\n%s", inventory)
	}
	if !strings.Contains(inventory, "urbex_app_port: \"8080\"") {
		t.Errorf("expected the public service's port:\n%s", inventory)
	}
	if strings.Contains(inventory, "urbex_app_port: \"9090\"") {
		t.Errorf("a non-public service must not get a port:\n%s", inventory)
	}
}

func TestWriteInventory_NoQuickTunnelNoPublicGroup(t *testing.T) {
	repoPath := t.TempDir()
	ansibleDir := filepath.Join(repoPath, "ansible", "projects", "my-app", "staging")
	services := []manifest.Service{{Name: "api", Runtime: "go", Port: 8080, Public: true}}
	allocations := map[string]allocation.Entry{
		Hostname("my-app", "api", "staging"): {IP: "192.168.1.210", VMID: 9100},
	}

	// quickTunnel: false - e.g. Cloudflare is configured, or
	// cloudflare.quickTunnel isn't set - must leave the group out
	// entirely: an empty group still makes Ansible warn about an
	// unmatched host pattern.
	if err := writeInventory(repoPath, ansibleDir, "my-app", "staging", services, allocations, bootstrap.Endpoints{}, true, false); err != nil {
		t.Fatalf("writeInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ansibleDir, "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "public_service") {
		t.Errorf("expected no public_service group without quickTunnel:\n%s", data)
	}
}
