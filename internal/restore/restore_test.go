package restore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

type fakePVE struct {
	machines  []proxmoxapi.Machine
	after     []proxmoxapi.Machine // Machines after the restore, when set
	backups   []proxmoxapi.Backup
	restoreEr error
	calls     []string
	restored  bool
}

func (f *fakePVE) Machines(context.Context) ([]proxmoxapi.Machine, error) {
	if f.restored && f.after != nil {
		return f.after, nil
	}
	return f.machines, nil
}

func (f *fakePVE) Backups(_ context.Context, node, storage string, vmid int) ([]proxmoxapi.Backup, error) {
	f.calls = append(f.calls, "backups "+node+" "+storage)
	return f.backups, nil
}

func (f *fakePVE) StopMachine(_ context.Context, _, kind string, vmid int) error {
	f.calls = append(f.calls, "stop "+kind)
	return nil
}

func (f *fakePVE) StartMachine(_ context.Context, _, kind string, vmid int) error {
	f.calls = append(f.calls, "start "+kind)
	return nil
}

func (f *fakePVE) RestoreMachine(_ context.Context, _, kind string, vmid int, volid, storage string) error {
	f.calls = append(f.calls, "restore "+kind+" "+volid+" "+storage)
	f.restored = true
	return f.restoreEr
}

var (
	t25 = time.Date(2026, 11, 25, 6, 1, 0, 0, time.UTC)
	t26 = time.Date(2026, 11, 26, 6, 1, 0, 0, time.UTC)
)

func gitea(status string) []proxmoxapi.Machine {
	return []proxmoxapi.Machine{
		{VMID: 9090, Name: "urbex-gitea", Kind: "lxc", Node: "pve", Pool: "urbex", Status: "stopped", Template: true},
		{VMID: 9000, Name: "urbex-gitea", Kind: "lxc", Node: "pve", Pool: "urbex", Status: status},
		{VMID: 100, Name: "pbs", Kind: "lxc", Node: "pve", Status: "running"},
	}
}

func twoBackups() []proxmoxapi.Backup {
	return []proxmoxapi.Backup{
		{VolID: "pbs:backup/ct/9000/2026-11-25", VMID: 9000, Kind: "lxc", Time: t25},
		{VolID: "pbs:backup/ct/9000/2026-11-26", VMID: 9000, Kind: "lxc", Time: t26},
	}
}

func opts() Options {
	return Options{Hostname: "urbex-gitea", Pool: "urbex", Storage: "pbs", DiskStorage: "local-zfs", Yes: true}
}

func TestMachine_StopsRestoresTheNewestAndStarts(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups()}
	var out bytes.Buffer
	if err := Machine(context.Background(), pve, &out, opts()); err != nil {
		t.Fatalf("Machine: %v", err)
	}
	want := []string{"backups pve pbs", "stop lxc", "restore lxc pbs:backup/ct/9000/2026-11-26 local-zfs", "start lxc"}
	if strings.Join(pve.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", pve.calls, want)
	}
	if !strings.Contains(out.String(), "restored to 2026-11-26") {
		t.Errorf("output: %s", out.String())
	}
}

func TestMachine_WithoutYesOnlySaysWhatItWouldDo(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups()}
	o := opts()
	o.Yes = false
	var out bytes.Buffer
	if err := Machine(context.Background(), pve, &out, o); err != nil {
		t.Fatalf("Machine: %v", err)
	}
	if len(pve.calls) != 1 {
		t.Errorf("calls = %q, want only the backups' listing", pve.calls)
	}
	if !strings.Contains(out.String(), "--yes") || !strings.Contains(out.String(), "2026-11-26") {
		t.Errorf("output: %s", out.String())
	}
}

func TestMachine_AtPicksTheNewestBackupBefore(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups()}
	o := opts()
	o.At = time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC)
	if err := Machine(context.Background(), pve, &bytes.Buffer{}, o); err != nil {
		t.Fatalf("Machine: %v", err)
	}
	if !strings.Contains(strings.Join(pve.calls, "|"), "restore lxc pbs:backup/ct/9000/2026-11-25 ") {
		t.Errorf("calls = %q, want the 25th's backup", pve.calls)
	}
}

func TestMachine_NoBackupBeforeAtListsThem(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups()}
	o := opts()
	o.At = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	err := Machine(context.Background(), pve, &bytes.Buffer{}, o)
	if err == nil || !strings.Contains(err.Error(), "2026-11-25 06:01, 2026-11-26 06:01") {
		t.Fatalf("err = %v, want the backups' times", err)
	}
	if len(pve.calls) != 1 {
		t.Errorf("calls = %q, want nothing changed", pve.calls)
	}
}

func TestMachine_NoBackup(t *testing.T) {
	pve := &fakePVE{machines: gitea("running")}
	err := Machine(context.Background(), pve, &bytes.Buffer{}, opts())
	if err == nil || !strings.Contains(err.Error(), "no backup of urbex-gitea (9000) on pbs") {
		t.Fatalf("err = %v", err)
	}
}

func TestMachine_AStoppedMachineStaysStopped(t *testing.T) {
	pve := &fakePVE{machines: gitea("stopped"), backups: twoBackups()}
	if err := Machine(context.Background(), pve, &bytes.Buffer{}, opts()); err != nil {
		t.Fatalf("Machine: %v", err)
	}
	for _, c := range pve.calls {
		if strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "start") {
			t.Errorf("calls = %q, want no stop or start", pve.calls)
		}
	}
}

func TestMachine_OnlyThePoolsMachines(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups()}
	o := opts()
	o.Hostname = "pbs"
	err := Machine(context.Background(), pve, &bytes.Buffer{}, o)
	if err == nil || !strings.Contains(err.Error(), "isn't in the pool urbex") {
		t.Fatalf("err = %v", err)
	}
	if len(pve.calls) != 0 {
		t.Errorf("calls = %q, want none", pve.calls)
	}
}

func TestMachine_AMachineThatIsGone(t *testing.T) {
	pve := &fakePVE{machines: gitea("running")}
	o := opts()
	o.Hostname = "urbex-komodo"
	err := Machine(context.Background(), pve, &bytes.Buffer{}, o)
	if err == nil || !strings.Contains(err.Error(), "no machine urbex-komodo in the pool urbex") {
		t.Fatalf("err = %v", err)
	}
}

func TestMachine_TwoMachinesWithTheName(t *testing.T) {
	ms := append(gitea("running"), proxmoxapi.Machine{VMID: 9010, Name: "urbex-gitea", Kind: "lxc", Pool: "urbex"})
	pve := &fakePVE{machines: ms}
	err := Machine(context.Background(), pve, &bytes.Buffer{}, opts())
	if err == nil || !strings.Contains(err.Error(), "9000, 9010") {
		t.Fatalf("err = %v", err)
	}
}

func TestMachine_SkipsABackupOfAnotherKind(t *testing.T) {
	bs := append(twoBackups(), proxmoxapi.Backup{VolID: "pbs:backup/vm/9000/2026-11-27", VMID: 9000, Kind: "qemu", Time: t26.Add(24 * time.Hour)})
	pve := &fakePVE{machines: gitea("running"), backups: bs}
	if err := Machine(context.Background(), pve, &bytes.Buffer{}, opts()); err != nil {
		t.Fatalf("Machine: %v", err)
	}
	if !strings.Contains(strings.Join(pve.calls, "|"), "2026-11-26 local-zfs") {
		t.Errorf("calls = %q, want the lxc backup", pve.calls)
	}
}

func TestMachine_ARestoreThatFailsSaysTheMachineIsStopped(t *testing.T) {
	pve := &fakePVE{machines: gitea("running"), backups: twoBackups(), restoreEr: errors.New("task failed")}
	err := Machine(context.Background(), pve, &bytes.Buffer{}, opts())
	if err == nil || !strings.Contains(err.Error(), "the machine is stopped") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(pve.calls, "|"), "start") {
		t.Errorf("calls = %q, want no start", pve.calls)
	}
}

func TestMachine_OutOfThePoolAfterTheRestoreIsntStarted(t *testing.T) {
	after := gitea("stopped")
	after[1].Pool = ""
	pve := &fakePVE{machines: gitea("running"), after: after, backups: twoBackups()}
	err := Machine(context.Background(), pve, &bytes.Buffer{}, opts())
	if err == nil || !strings.Contains(err.Error(), "no longer in the pool urbex") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.Join(pve.calls, "|"), "start") {
		t.Errorf("calls = %q, want no start", pve.calls)
	}
}

func TestParseAt(t *testing.T) {
	rome, _ := time.LoadLocation("Europe/Rome")
	cases := map[string]time.Time{
		"":                          {},
		"2026-11-25 06:00":          time.Date(2026, 11, 25, 6, 0, 0, 0, rome),
		"2026-11-25T06:00":          time.Date(2026, 11, 25, 6, 0, 0, 0, rome),
		"2026-11-25 06:00:30":       time.Date(2026, 11, 25, 6, 0, 30, 0, rome),
		"2026-11-25":                time.Date(2026, 11, 25, 23, 59, 59, 0, rome),
		"2026-11-25T05:00:00Z":      time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC),
		"2026-11-25T06:00:00+01:00": time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := ParseAt(in, rome)
		if err != nil {
			t.Errorf("ParseAt(%q): %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("ParseAt(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseAt("yesterday", rome); err == nil {
		t.Error("ParseAt(yesterday): want an error")
	}
}
