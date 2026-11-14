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
	"net"
	"os"
	"path/filepath"
	"sort"

	"github.com/urbforge/urbex-cli/internal/netalloc"
)

// Entry is one hostname's assigned VMID and IPv4 address.
type Entry struct {
	VMID int    `json:"vmid"`
	IP   string `json:"ip"`
}

// Ledger tracks assignments and the next VMID to hand out.
type Ledger struct {
	Entries  map[string]Entry `json:"entries"`
	NextVMID int              `json:"nextVmid"`
	// NextIPOffset is no longer what decides an address (Allocate takes
	// the lowest free one); kept so that ledgers read and write as before.
	NextIPOffset int `json:"nextIpOffset"`

	// firstIPOffset is the first host offset Allocate may hand out:
	// Load's defaultNextIPOffset.
	firstIPOffset int
}

// Load reads the ledger from path, or returns a fresh one seeded with the
// given defaults if the file doesn't exist yet.
func Load(path string, defaultNextVMID, defaultNextIPOffset int) (*Ledger, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Ledger{
			Entries:       map[string]Entry{},
			NextVMID:      defaultNextVMID,
			NextIPOffset:  defaultNextIPOffset,
			firstIPOffset: defaultNextIPOffset,
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
	l.firstIPOffset = defaultNextIPOffset
	return &l, nil
}

// Lookup returns the entry for hostname without allocating one if it's
// missing - used by commands that must not provision anything (e.g.
// urbex deploy, urbex status).
func (l *Ledger) Lookup(hostname string) (Entry, bool) {
	e, ok := l.Entries[hostname]
	return e, ok
}

// Allocate returns the existing entry for hostname if one was already
// assigned, or assigns a new VMID and the lowest free address of cidr
// from the first project offset: an address Remove freed is handed out
// again. The network address, the broadcast address and gateway ("" for
// none) never are.
func (l *Ledger) Allocate(hostname, cidr, gateway string) (Entry, error) {
	if e, ok := l.Entries[hostname]; ok {
		return e, nil
	}

	ip, err := l.freeIP(cidr, gateway)
	if err != nil {
		return Entry{}, fmt.Errorf("allocating an IP for %s: %w", hostname, err)
	}

	e := Entry{VMID: l.NextVMID, IP: ip}
	l.Entries[hostname] = e
	l.NextVMID++
	return e, nil
}

// freeIP is the lowest address of cidr, from the first project offset,
// that no entry holds and that isn't the network's, its broadcast or
// gateway.
func (l *Ledger) freeIP(cidr, gateway string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parsing cidr %q: %w", cidr, err)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 || ones > 30 {
		return "", fmt.Errorf("cidr %q is no IPv4 network with room for hosts", cidr)
	}
	broadcast := 1<<(bits-ones) - 1
	used := map[string]bool{gateway: true}
	for _, e := range l.Entries {
		used[e.IP] = true
	}
	first := max(l.firstIPOffset, 1)
	for offset := first; offset < broadcast; offset++ {
		addrs, _, err := netalloc.Allocate(cidr, offset, 1)
		if err != nil {
			return "", err
		}
		if !used[addrs[0]] {
			return addrs[0], nil
		}
	}
	return "", fmt.Errorf("no free address left in %s from host offset %d (%d addresses, all in use): 'urbex destroy' frees the addresses of the environments it removes",
		cidr, first, max(broadcast-first, 0))
}

// Remove deletes hostname's entry, if any. Used when infrastructure is
// torn down (e.g. urbex destroy) so future Lookup/status calls correctly
// report it as no longer allocated.
func (l *Ledger) Remove(hostname string) {
	delete(l.Entries, hostname)
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

// Hostnames returns the hostnames with an allocation, sorted.
func (l *Ledger) Hostnames() []string {
	names := make([]string, 0, len(l.Entries))
	for h := range l.Entries {
		names = append(names, h)
	}
	sort.Strings(names)
	return names
}
