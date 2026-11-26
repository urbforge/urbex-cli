package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

type fakeBackup struct {
	perms, userPerms map[string]map[string]bool
	userErr, err     error
	storage          proxmoxapi.Storage
	storageErr       error
}

func (f fakeBackup) Permissions(context.Context) (map[string]map[string]bool, error) {
	return f.perms, f.err
}
func (f fakeBackup) PermissionsOf(context.Context, string) (map[string]map[string]bool, error) {
	return f.userPerms, f.userErr
}
func (f fakeBackup) StorageStatus(context.Context, string, string) (proxmoxapi.Storage, error) {
	return f.storage, f.storageErr
}

func TestBackupChecks(t *testing.T) {
	ctx := context.Background()
	token := "urbex-backup@pve!backup=00000000-1111-2222-3333-444444444444"
	cfg := &platform.Config{Proxmox: platform.ProxmoxConfig{Node: "pve", Pool: "urbex"}}
	cfg.Backups.Proxmox.Storage = "pbs"
	run := func(b BackupReader, tok string, c *platform.Config) []Check {
		r := &Report{}
		BackupChecks(ctx, b, tok, c, r)
		return r.Checks
	}
	full := map[string]map[string]bool{
		"/pool/urbex":  {"Pool.Audit": true, "VM.Audit": true, "VM.Backup": true},
		"/storage/pbs": {"Datastore.AllocateSpace": true, "Datastore.Audit": true},
	}
	goodStorage := proxmoxapi.Storage{Name: "pbs", Type: "pbs", Content: []string{"backup"}, Active: true, AvailGB: 900, TotalGB: 1000}

	off := &platform.Config{}
	if c := run(nil, "", off); len(c) != 1 || c[0].Status != Warn || !strings.Contains(c[0].Detail, "nothing backs the machines up") {
		t.Errorf("off: %+v", c)
	}
	if c := run(nil, "", cfg); len(c) != 1 || c[0].Status != Fail || !strings.Contains(c[0].Fix, "proxmoxBackupToken") {
		t.Errorf("no token: %+v", c)
	}
	if c := run(fakeBackup{}, "00000000-1111", cfg); c[0].Status != Fail || !strings.Contains(c[0].Detail, "token's id") {
		t.Errorf("secret only: %+v", c)
	}
	if c := run(fakeBackup{err: errors.New("401 authentication failure")}, token, cfg); c[0].Status != Fail || !strings.Contains(c[0].Detail, "401") {
		t.Errorf("refused: %+v", c)
	}
	c := run(fakeBackup{perms: full, userPerms: full, storage: goodStorage}, token, cfg)
	if len(c) != 2 || c[0].Status != OK || c[1].Status != OK || !strings.Contains(c[1].Detail, "pbs (pbs), 900 of 1000 GB free") {
		t.Errorf("all good: %+v", c)
	}
	// Granted on / to the user and the token: propagates down.
	root := map[string]map[string]bool{"/": {"Pool.Audit": true, "VM.Audit": true, "VM.Backup": true, "Datastore.AllocateSpace": true, "Datastore.Audit": true}}
	if c := run(fakeBackup{perms: root, userPerms: root, storage: goodStorage}, token, cfg); c[0].Status != OK {
		t.Errorf("granted on /: %+v", c)
	}

	// The lab's twice-seen case: the user has the storage, the
	// privilege-separated token doesn't.
	tokenOnlyPool := map[string]map[string]bool{"/pool/urbex": full["/pool/urbex"]}
	c = run(fakeBackup{perms: tokenOnlyPool, userPerms: full, storage: goodStorage}, token, cfg)
	if len(c) != 1 || c[0].Status != Fail ||
		!strings.Contains(c[0].Detail, "the token lacks Datastore.AllocateSpace, Datastore.Audit on /storage/pbs, which its user urbex-backup@pve has") ||
		!strings.Contains(c[0].Fix, "--tokens 'urbex-backup@pve!backup'") {
		t.Errorf("token lacks what its user has: %+v", c)
	}
	// Both lack VM.Backup.
	noBackup := map[string]map[string]bool{"/pool/urbex": {"Pool.Audit": true, "VM.Audit": true}, "/storage/pbs": full["/storage/pbs"]}
	c = run(fakeBackup{perms: noBackup, userPerms: noBackup, storage: goodStorage}, token, cfg)
	if c[0].Status != Fail || !strings.Contains(c[0].Detail, "the token and its user urbex-backup@pve lack VM.Backup on /pool/urbex") ||
		!strings.Contains(c[0].Fix, "--users urbex-backup@pve") {
		t.Errorf("both lack: %+v", c)
	}
	// The user's privileges unreadable: only the token's are named.
	c = run(fakeBackup{perms: noBackup, userErr: errors.New("403"), storage: goodStorage}, token, cfg)
	if c[0].Status != Fail || !strings.Contains(c[0].Detail, "the token lacks VM.Backup on /pool/urbex") {
		t.Errorf("user unreadable: %+v", c)
	}

	for name, s := range map[string]fakeBackup{
		"not on the node":   {storageErr: errors.New("500 storage 'pbs' does not exist")},
		"no backup content": {storage: proxmoxapi.Storage{Name: "pbs", Content: []string{"images", "rootdir"}, Active: true}},
		"inactive":          {storage: proxmoxapi.Storage{Name: "pbs", Content: []string{"backup"}}},
	} {
		s.perms, s.userPerms = full, full
		if c := run(s, token, cfg); len(c) != 2 || c[1].Status != Fail || c[1].Name != "backup storage" {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := run(nil, "", nil); len(c) != 0 {
		t.Error("checked without a config")
	}
}
