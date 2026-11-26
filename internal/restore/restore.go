// Package restore answers `urbex restore`: for now a whole machine from
// the Proxmox layer, ADR-0036 point 3 and 7 (in urbforge/urbex) - the
// machine's newest backup at or before a time, restored onto the same
// VMID, replacing the machine.
package restore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// Proxmox is the subset of proxmoxapi.Client a restore uses.
type Proxmox interface {
	Machines(ctx context.Context) ([]proxmoxapi.Machine, error)
	Backups(ctx context.Context, node, storage string, vmid int) ([]proxmoxapi.Backup, error)
	StopMachine(ctx context.Context, node, kind string, vmid int) error
	StartMachine(ctx context.Context, node, kind string, vmid int) error
	RestoreMachine(ctx context.Context, node, kind string, vmid int, volid, storage string) error
}

// Options is one machine's restore.
type Options struct {
	// Hostname is the machine's name on Proxmox, e.g. urbex-gitea or
	// my-app-api-prod.
	Hostname string
	// Pool is the platform's pool: only its machines are restored.
	Pool string
	// Storage holds the backups (backups.proxmox.storage).
	Storage string
	// DiskStorage is where the restored machine's disks go
	// (proxmox.storagePool), the same as urbex creates them on.
	DiskStorage string
	// At picks the newest backup at or before it; zero is the newest.
	At time.Time
	// Yes confirms: without it Machine only says what it would do.
	Yes bool
}

// Machine restores one machine of the pool from its backup on
// o.Storage: it stops the machine, restores the backup onto its VMID
// (replacing its disks and configuration), and starts it again if it was
// running.
func Machine(ctx context.Context, pve Proxmox, out io.Writer, o Options) error {
	machines, err := pve.Machines(ctx)
	if err != nil {
		return fmt.Errorf("listing proxmox machines: %w", err)
	}
	m, err := find(machines, o.Hostname, o.Pool)
	if err != nil {
		return err
	}
	backups, err := pve.Backups(ctx, m.Node, o.Storage, m.VMID)
	if err != nil {
		return fmt.Errorf("listing the backups of %s (%d) on %s: %w", m.Name, m.VMID, o.Storage, err)
	}
	b, err := pick(backups, m, o)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s (%s %d, %s): backup of %s, %s\n", m.Name, m.Kind, m.VMID, m.Status, b.Time.Format("2006-01-02 15:04 MST"), b.VolID)
	if !o.Yes {
		fmt.Fprintf(out, "Restoring replaces the machine's disks and configuration with the backup's: whatever changed since %s is lost.\nRun again with --yes to restore it.\n", b.Time.Format("2006-01-02 15:04 MST"))
		return nil
	}

	wasRunning := m.Status == "running"
	if wasRunning {
		fmt.Fprintf(out, "Stopping %s...\n", m.Name)
		if err := pve.StopMachine(ctx, m.Node, m.Kind, m.VMID); err != nil {
			return fmt.Errorf("stopping %s: %w", m.Name, err)
		}
	}
	fmt.Fprintf(out, "Restoring %s from %s...\n", m.Name, b.VolID)
	if err := pve.RestoreMachine(ctx, m.Node, m.Kind, m.VMID, b.VolID, o.DiskStorage); err != nil {
		return fmt.Errorf("restoring %s (%d) from %s: %w; the machine is stopped, its old disks may be gone: run the restore again", m.Name, m.VMID, b.VolID, err)
	}
	if err := stillInPool(ctx, pve, m, o.Pool); err != nil {
		return err
	}
	if wasRunning {
		fmt.Fprintf(out, "Starting %s...\n", m.Name)
		if err := pve.StartMachine(ctx, m.Node, m.Kind, m.VMID); err != nil {
			return fmt.Errorf("starting %s after the restore: %w", m.Name, err)
		}
	}
	fmt.Fprintf(out, "%s restored to %s.\n", m.Name, b.Time.Format("2006-01-02 15:04 MST"))
	return nil
}

// find returns the pool's machine named hostname, never a template.
func find(machines []proxmoxapi.Machine, hostname, pool string) (proxmoxapi.Machine, error) {
	var found []proxmoxapi.Machine
	for _, m := range machines {
		if m.Name == hostname && m.Pool == pool && !m.Template {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		for _, m := range machines {
			if m.Name == hostname && !m.Template {
				return proxmoxapi.Machine{}, fmt.Errorf("%s (%d) isn't in the pool %s: urbex restores only the pool's machines", hostname, m.VMID, pool)
			}
		}
		return proxmoxapi.Machine{}, fmt.Errorf("no machine %s in the pool %s on Proxmox: restoring onto a machine that is gone isn't supported yet, recreate it first (urbex bootstrap or urbex apply)", hostname, pool)
	default:
		ids := make([]string, len(found))
		for i, m := range found {
			ids[i] = fmt.Sprint(m.VMID)
		}
		return proxmoxapi.Machine{}, fmt.Errorf("%d machines named %s in the pool %s (%s): rename or remove the extra ones first", len(found), hostname, pool, strings.Join(ids, ", "))
	}
}

// pick returns the newest of backups (oldest first) at or before o.At,
// of the same kind as m.
func pick(backups []proxmoxapi.Backup, m proxmoxapi.Machine, o Options) (proxmoxapi.Backup, error) {
	var best *proxmoxapi.Backup
	for i, b := range backups {
		if b.Kind != "" && b.Kind != m.Kind {
			continue
		}
		if !o.At.IsZero() && b.Time.After(o.At) {
			continue
		}
		best = &backups[i]
	}
	if best != nil {
		return *best, nil
	}
	if len(backups) == 0 {
		return proxmoxapi.Backup{}, fmt.Errorf("no backup of %s (%d) on %s", m.Name, m.VMID, o.Storage)
	}
	times := make([]string, 0, len(backups))
	for _, b := range backups {
		times = append(times, b.Time.Format("2006-01-02 15:04"))
	}
	return proxmoxapi.Backup{}, fmt.Errorf("no backup of %s (%d) on %s at or before %s; there are: %s", m.Name, m.VMID, o.Storage, o.At.Format("2006-01-02 15:04 MST"), strings.Join(times, ", "))
}

// stillInPool checks the restored machine is still in the pool: the
// backup's configuration doesn't carry the pool.
func stillInPool(ctx context.Context, pve Proxmox, m proxmoxapi.Machine, pool string) error {
	machines, err := pve.Machines(ctx)
	if err != nil {
		return fmt.Errorf("listing proxmox machines after the restore: %w", err)
	}
	for _, r := range machines {
		if r.VMID == m.VMID && r.Pool == pool {
			return nil
		}
	}
	return fmt.Errorf("%s (%d) is restored but no longer in the pool %s: add it back in Proxmox (Datacenter > Pools > %s > Members) before starting it", m.Name, m.VMID, pool, pool)
}

// ParseAt reads --at: a date and time in the local zone ("2026-11-25
// 06:00", "2026-11-25T06:00"), RFC 3339, or a date alone, meaning the end
// of that day.
func ParseAt(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return d.Add(24*time.Hour - time.Second), nil
	}
	return time.Time{}, fmt.Errorf("--at %q: use a date and time, e.g. \"2026-11-25 06:00\", or a date", s)
}
