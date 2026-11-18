package provision

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
)

func writeState(t *testing.T, dir string, hostnames ...string) {
	t.Helper()
	var res []string
	for i, h := range hostnames {
		res = append(res, `{"module":"module.service[\"`+h+`\"]","mode":"managed","type":"proxmox_virtual_environment_container","name":"this","instances":[{"attributes":{"vm_id":`+string(rune('0'+i))+`}}]}`)
	}
	data := `{"version":4,"resources":[` + strings.Join(res, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateHostnames(t *testing.T) {
	dir := t.TempDir()
	if got, err := stateHostnames(dir); err != nil || len(got) != 0 {
		t.Fatalf("no state: %v, %v", got, err)
	}
	writeState(t, dir, "shop-api-prod", "shop-board-prod")
	got, err := stateHostnames(dir)
	if err != nil || !got["shop-api-prod"] || !got["shop-board-prod"] || len(got) != 2 {
		t.Errorf("got %v, %v", got, err)
	}
}

// An application removed from urbex.yaml and deleted by apply frees its
// ledger entry; one still declared keeps it, and so does what stays in the
// state (urbforge/urbex-cli#115).
func TestFreeGone(t *testing.T) {
	repo := t.TempDir()
	ledgerPath := filepath.Join(repo, "state", "allocations.json")
	l, _ := allocation.Load(ledgerPath, 9100, 210)
	for _, h := range []string{"shop-api-prod", "shop-board-prod", "shop-db-prod", "other-web-prod"} {
		if _, err := l.Allocate(h, "192.168.1.0/24", "192.168.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Save(ledgerPath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := &Orchestrator{Config: validPlatformConfig(), Out: &out}
	before := map[string]bool{"shop-api-prod": true, "shop-board-prod": true, "shop-db-prod": true}
	after := map[string]bool{"shop-api-prod": true}
	keep := map[string]bool{"shop-api-prod": true, "shop-db-prod": true} // db: deleted outside, still declared
	if err := o.freeGone(Options{GitOpsRepoPath: repo}, before, after, keep); err != nil {
		t.Fatal(err)
	}
	l, _ = allocation.Load(ledgerPath, 9100, 210)
	for h, want := range map[string]bool{"shop-api-prod": true, "shop-board-prod": false, "shop-db-prod": true, "other-web-prod": true} {
		if _, ok := l.Lookup(h); ok != want {
			t.Errorf("%s in the ledger: %v, want %v", h, ok, want)
		}
	}
	if !strings.Contains(out.String(), "Freed the addresses of shop-board-prod") {
		t.Errorf("output: %q", out.String())
	}
}
