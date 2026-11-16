package allocation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllocate_IsIdempotent(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	first, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	second, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if first != second {
		t.Errorf("expected the same allocation on repeated calls, got %+v then %+v", first, second)
	}
	if first.VMID != 9100 || first.IP != "192.168.1.210" {
		t.Errorf("unexpected first allocation: %+v", first)
	}
}

func TestAllocate_DistinctHostnamesGetDistinctSlots(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	b, err := l.Allocate("my-app-worker-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if a.VMID == b.VMID || a.IP == b.IP {
		t.Errorf("expected distinct allocations, got %+v and %+v", a, b)
	}
	if b.VMID != a.VMID+1 {
		t.Errorf("expected sequential VMIDs, got %d then %d", a.VMID, b.VMID)
	}
}

func TestSaveAndLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "allocations.json")

	l, err := Load(path, 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if err := l.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(path, 9100, 210)
	if err != nil {
		t.Fatalf("Load (reloaded): %v", err)
	}

	got, err := reloaded.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate (reloaded): %v", err)
	}
	if got.VMID != 9100 || got.IP != "192.168.1.210" {
		t.Errorf("expected the persisted allocation to survive a reload, got %+v", got)
	}
	if reloaded.NextVMID != 9101 {
		t.Errorf("expected NextVMID to be persisted as 9101, got %d", reloaded.NextVMID)
	}
}

func TestLookup(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, ok := l.Lookup("my-app-api-staging"); ok {
		t.Fatal("expected Lookup to report not-found before any Allocate call")
	}

	want, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	got, ok := l.Lookup("my-app-api-staging")
	if !ok {
		t.Fatal("expected Lookup to find the entry after Allocate")
	}
	if got != want {
		t.Errorf("Lookup = %+v, want %+v", got, want)
	}
}

func TestRemove(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := l.Allocate("my-app-api-staging", "192.168.1.0/24", "192.168.1.1"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	l.Remove("my-app-api-staging")

	if _, ok := l.Lookup("my-app-api-staging"); ok {
		t.Error("expected the entry to be gone after Remove")
	}

	// Removing an already-absent hostname must not panic or error.
	l.Remove("never-existed")
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.NextVMID != 9100 || l.NextIPOffset != 210 {
		t.Errorf("unexpected defaults: %+v", l)
	}
	if len(l.Entries) != 0 {
		t.Errorf("expected no entries, got %v", l.Entries)
	}
}

func TestAllocate_ReusesAFreedAddress(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, _ := l.Allocate("a", "192.168.1.0/24", "192.168.1.1")
	if _, err := l.Allocate("b", "192.168.1.0/24", "192.168.1.1"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	l.Remove("a")
	c, err := l.Allocate("c", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if c.IP != a.IP {
		t.Errorf("c got %s, want a's freed %s", c.IP, a.IP)
	}
	if c.VMID == a.VMID {
		t.Errorf("c reused a's VMID %d: VMIDs only go forwards", c.VMID)
	}
}

func TestAllocate_NeverTheBroadcastNetworkOrGateway(t *testing.T) {
	// A /29: offsets 1..6 are hosts, 0 the network, 7 the broadcast.
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, h := range []string{"h1", "h2", "h3", "h4", "h5"} {
		e, err := l.Allocate(h, "10.0.0.8/29", "10.0.0.9")
		if err != nil {
			t.Fatalf("Allocate %s: %v", h, err)
		}
		got = append(got, e.IP)
	}
	want := []string{"10.0.0.10", "10.0.0.11", "10.0.0.12", "10.0.0.13", "10.0.0.14"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
	_, err = l.Allocate("h6", "10.0.0.8/29", "10.0.0.9")
	if err == nil || !strings.Contains(err.Error(), "no free address") || !strings.Contains(err.Error(), "urbex destroy") {
		t.Errorf("a full network should say so and what frees addresses, got %v", err)
	}
}

// A ledger written before addresses were reused: its nextIpOffset is past
// the network's end, and an entry sits on the broadcast.
func TestAllocate_OldLedgerPastTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allocations.json")
	old := `{"entries":{"web":{"vmid":9145,"ip":"192.168.1.255"},"api":{"vmid":9144,"ip":"192.168.1.210"}},"nextVmid":9146,"nextIpOffset":256}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(path, 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, err := l.Allocate("new", "192.168.1.0/24", "192.168.1.1")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if e.IP != "192.168.1.211" || e.VMID != 9146 {
		t.Errorf("got %+v, want 192.168.1.211 and VMID 9146", e)
	}
}
