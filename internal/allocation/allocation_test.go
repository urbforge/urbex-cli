package allocation

import (
	"path/filepath"
	"testing"
)

func TestAllocate_IsIdempotent(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "allocations.json"), 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	first, err := l.Allocate("my-app-api-staging", "192.168.1.0/24")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	second, err := l.Allocate("my-app-api-staging", "192.168.1.0/24")
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

	a, err := l.Allocate("my-app-api-staging", "192.168.1.0/24")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	b, err := l.Allocate("my-app-worker-staging", "192.168.1.0/24")
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
	if _, err := l.Allocate("my-app-api-staging", "192.168.1.0/24"); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if err := l.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(path, 9100, 210)
	if err != nil {
		t.Fatalf("Load (reloaded): %v", err)
	}

	got, err := reloaded.Allocate("my-app-api-staging", "192.168.1.0/24")
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

	want, err := l.Allocate("my-app-api-staging", "192.168.1.0/24")
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
	if _, err := l.Allocate("my-app-api-staging", "192.168.1.0/24"); err != nil {
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
