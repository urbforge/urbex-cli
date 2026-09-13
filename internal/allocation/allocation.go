// Package allocation persists a deterministic VMID/IP assignment per
// project-service hostname, shared across all projects in a GitOps repo,
// so re-running urbex plan/apply always reuses the same VMID and IP for a
// given hostname instead of drifting on every run. Stored as
// state/allocations.json in the GitOps repo, alongside the encrypted
// Terraform state (see docs/decisions/0013-terraform-state-in-gitops-repo.md
// in urbforge/urbex).
package allocation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/internal/netalloc"
)

// Entry is one hostname's assigned VMID and IPv4 address.
type Entry struct {
	VMID int    `json:"vmid"`
	IP   string `json:"ip"`
}

// Ledger tracks assignments and the next values to hand out.
type Ledger struct {
	Entries      map[string]Entry `json:"entries"`
	NextVMID     int              `json:"nextVmid"`
	NextIPOffset int              `json:"nextIpOffset"`
}

// Load reads the ledger from path, or returns a fresh one seeded with the
// given defaults if the file doesn't exist yet.
func Load(path string, defaultNextVMID, defaultNextIPOffset int) (*Ledger, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Ledger{
			Entries:      map[string]Entry{},
			NextVMID:     defaultNextVMID,
			NextIPOffset: defaultNextIPOffset,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if l.Entries == nil {
		l.Entries = map[string]Entry{}
	}
	return &l, nil
}

// Allocate returns the existing entry for hostname if one was already
// assigned, or assigns and persists a new VMID/IP for it from cidr.
func (l *Ledger) Allocate(hostname, cidr string) (Entry, error) {
	if e, ok := l.Entries[hostname]; ok {
		return e, nil
	}

	addrs, _, err := netalloc.Allocate(cidr, l.NextIPOffset, 1)
	if err != nil {
		return Entry{}, fmt.Errorf("allocating an IP for %s: %w", hostname, err)
	}

	e := Entry{VMID: l.NextVMID, IP: addrs[0]}
	l.Entries[hostname] = e
	l.NextVMID++
	l.NextIPOffset++
	return e, nil
}

// Save writes the ledger to path as indented JSON, creating parent
// directories as needed.
func (l *Ledger) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
