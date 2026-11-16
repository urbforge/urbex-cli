package status

import (
	"context"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type fakeProxmox struct {
	hostnames []string
}

func (f *fakeProxmox) ListContainerHostnames(ctx context.Context, node string) ([]string, error) {
	return f.hostnames, nil
}

func TestBase_ReportsPresenceOfEachBaseService(t *testing.T) {
	proxmox := &fakeProxmox{hostnames: []string{"urbex-gitea", "urbex-keycloak"}}

	statuses, err := Base(context.Background(), proxmox, "pve", bootstrap.BaseServices)
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	if len(statuses) != 5 {
		t.Fatalf("expected 5 base services, got %d", len(statuses))
	}

	byHostname := map[string]BaseServiceStatus{}
	for _, s := range statuses {
		byHostname[s.Hostname] = s
	}

	if !byHostname["urbex-gitea"].Present {
		t.Error("expected urbex-gitea to be present")
	}
	if !byHostname["urbex-keycloak"].Present {
		t.Error("expected urbex-keycloak to be present")
	}
	if byHostname["urbex-komodo"].Present {
		t.Error("expected urbex-komodo to be absent")
	}
}

func TestProject_ReportsAllocatedAndProvisioned(t *testing.T) {
	ledger, err := allocation.Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := ledger.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	services := []manifest.Service{
		{Name: "api", Runtime: "go", Port: 8080},
		{Name: "worker", Runtime: "go", Port: 9000},
	}

	// api is provisioned on Proxmox; worker is neither allocated nor provisioned.
	proxmox := &fakeProxmox{hostnames: []string{"my-app-api-staging"}}

	statuses, err := Project(context.Background(), proxmox, "pve", ledger, "my-app", "staging", services)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	if !statuses[0].Allocated || !statuses[0].Provisioned {
		t.Errorf("expected api to be allocated and provisioned, got %+v", statuses[0])
	}
	if statuses[1].Allocated || statuses[1].Provisioned {
		t.Errorf("expected worker to be neither allocated nor provisioned, got %+v", statuses[1])
	}
}
