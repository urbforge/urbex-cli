package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// filesOrchestrator is a platform whose file layer goes to target, its
// credentials in a temporary home.
func filesOrchestrator(t *testing.T, target string) (*Orchestrator, *strings.Builder) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	o := backupOrchestrator("", "")
	o.Config.Proxmox.VMIDBase = 9000
	o.Config.Proxmox.Network = platform.NetworkConfig{CIDR: "192.168.1.0/24", Gateway: "192.168.1.1", BaseHostOffset: 200}
	o.Config.Storage.Shares = map[string]platform.Share{"nas": {Path: "/mnt/pve/nas"}}
	o.Config.Backups.Files.Target = target
	out := &strings.Builder{}
	o.Out = out
	return o, out
}

// The password is generated once, kept, and printed that one time.
func TestEnsureBackupPassword(t *testing.T) {
	o, out := filesOrchestrator(t, "share:nas/urbex")
	if err := o.ensureBackupPassword(); err != nil {
		t.Fatal(err)
	}
	pw := o.Credentials.BackupPassword
	if len(pw) < 32 || !strings.Contains(out.String(), pw) || !strings.Contains(out.String(), "backupSecretsStored") {
		t.Fatalf("password %q not generated and shown:\n%s", pw, out)
	}
	out.Reset()
	if err := o.ensureBackupPassword(); err != nil {
		t.Fatal(err)
	}
	if o.Credentials.BackupPassword != pw || out.Len() != 0 {
		t.Errorf("a second run changed or showed the password again: %q\n%s", o.Credentials.BackupPassword, out)
	}
	// Off: nothing generated.
	off, _ := filesOrchestrator(t, "")
	if err := off.ensureBackupPassword(); err != nil || off.Credentials.BackupPassword != "" {
		t.Errorf("a password without the file layer: %q, %v", off.Credentials.BackupPassword, err)
	}
}

// One Stack on each LXC with data, in a compose project of its own; the
// secrets only as Komodo variables in their environment.
func TestFileBackupStacks(t *testing.T) {
	for _, tc := range []struct{ target, repo, extra string }{
		{"share:nas/urbex", "/repo/urbex", "URBEX_BACKUP_REPO=/mnt/urbex-backup"},
		{"s3:https://s3.example.com/bucket/urbex", "s3:https://s3.example.com/bucket/urbex", "AWS_SECRET_ACCESS_KEY=[[URBEX_BACKUP_S3_SECRET_ACCESS_KEY]]"},
	} {
		o, _ := filesOrchestrator(t, tc.target)
		if got := o.backupRepository(); got != tc.repo {
			t.Errorf("%s: repository %q, want %q", tc.target, got, tc.repo)
		}
		stacks := o.fileBackupStacks()
		if len(stacks) != 3 {
			t.Fatalf("%s: %d stacks", tc.target, len(stacks))
		}
		for i, svc := range FileBackupServices {
			st := stacks[i]
			if st.Name != "urbex-backup-"+svc || st.Server != "urbex-"+svc || st.Dir != "backup-"+svc || st.ProjectName != FileBackupProject {
				t.Errorf("stack %+v", st)
			}
			if !strings.Contains(st.Environment, "RESTIC_PASSWORD=[[URBEX_BACKUP_PASSWORD]]") || !strings.Contains(st.Environment, tc.extra) {
				t.Errorf("%s: environment %q", tc.target, st.Environment)
			}
			if len(st.Secrets) != 0 {
				t.Errorf("secrets in the GitOps repo: %v", st.Secrets)
			}
		}
		deploys, err := o.platformStackDeploys(stacks)
		if err != nil {
			t.Fatal(err)
		}
		if deploys[0].cfg.ProjectName != FileBackupProject || deploys[0].cfg.ComposeCmdWrapper != "" {
			t.Errorf("deploy %+v", deploys[0].cfg)
		}
	}
	if o, _ := filesOrchestrator(t, ""); o.fileBackupStacks() != nil {
		t.Error("stacks without the file layer")
	}
}

// fakeMounts are the platform LXCs' mount points.
type fakeMounts map[int][]proxmoxapi.MountPoint

func (f fakeMounts) ListContainerHostnames(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f fakeMounts) MountPoints(_ context.Context, _ string, vmid int) ([]proxmoxapi.MountPoint, error) {
	if vmid == 0 {
		return nil, errors.New("no such LXC")
	}
	return f[vmid], nil
}

// A share target waits until the operator has bound it into the forge's,
// Komodo's and Keycloak's LXCs: a command for each that lacks it, with
// its next free key.
func TestFileBackupsReady_Share(t *testing.T) {
	o, out := filesOrchestrator(t, "share:nas/urbex")
	mounts := fakeMounts{9001: {{Key: "mp0", Volume: "local-lvm:x", Path: "/srv"}}}
	o.Proxmox = mounts
	ready, err := o.fileBackupsReady(context.Background())
	if err != nil || ready {
		t.Fatalf("ready %v, err %v", ready, err)
	}
	for _, want := range []string{
		"pct set 9000 -mp0 /mnt/pve/nas,mp=/mnt/urbex-backup",
		"pct set 9001 -mp1 /mnt/pve/nas,mp=/mnt/urbex-backup",
		"pct set 9003 -mp0 /mnt/pve/nas,mp=/mnt/urbex-backup",
		"root@pam",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "pct set 9002") {
		t.Errorf("Technitium has no data to back up:\n%s", out)
	}
	for _, vmid := range []int{9000, 9001, 9003} {
		mounts[vmid] = append(mounts[vmid], proxmoxapi.MountPoint{Key: "mp5", Volume: "/mnt/pve/nas/", Path: "/mnt/urbex-backup"})
	}
	out.Reset()
	if ready, err := o.fileBackupsReady(context.Background()); err != nil || !ready || out.Len() != 0 {
		t.Errorf("bound: ready %v, err %v\n%s", ready, err, out)
	}
}

func TestFileBackupsReady_Bucket(t *testing.T) {
	o, _ := filesOrchestrator(t, "s3:https://s3.example.com/bucket")
	if _, err := o.fileBackupsReady(context.Background()); !errors.Is(err, ErrNoBackupS3Key) {
		t.Fatalf("err = %v, want ErrNoBackupS3Key", err)
	}
	o.Credentials.BackupS3AccessKeyID, o.Credentials.BackupS3SecretAccessKey = "id", "secret"
	if ready, err := o.fileBackupsReady(context.Background()); err != nil || !ready {
		t.Errorf("ready %v, err %v", ready, err)
	}
}

// With the file layer alone: its variables (the password and the bucket's
// key secret), the Action's configuration with the Stacks, no Proxmox
// token; switched off, its Stacks and variables go.
func TestSetupBackups_FileLayer(t *testing.T) {
	o, _ := filesOrchestrator(t, "s3:https://s3.example.com/bucket")
	o.Credentials.BackupPassword, o.Credentials.BackupS3AccessKeyID, o.Credentials.BackupS3SecretAccessKey = "pw", "id", "sk"
	o.filesReady = true
	fp := &fakePlatform{}
	k := &fakeKomodo{fp}
	if err := o.setupBackups(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		BackupRepositoryVariable:  "s3:https://s3.example.com/bucket",
		BackupPasswordVariable:    "secret:pw",
		BackupS3KeyIDVariable:     "secret:id",
		BackupS3SecretKeyVariable: "secret:sk",
	} {
		if got := fp.variables[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, ok := fp.variables[BackupTokenVariable]; ok {
		t.Error("a Proxmox token without the Proxmox layer")
	}
	var cfg backupConfig
	if err := json.Unmarshal([]byte(fp.variables[BackupConfigVariable]), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ProxmoxOn || cfg.Files == nil || len(cfg.Files.Stacks) != 3 || cfg.Files.Keep != (backupKeep{7, 4, 6}) ||
		cfg.Files.Stacks[0].Stack != "urbex-backup-gitea" || cfg.Files.Stacks[0].Host != "urbex-gitea" ||
		strings.Join(cfg.Files.Stacks[0].Paths, ",") != "/data/gitea" {
		t.Errorf("config = %+v", cfg)
	}
	if _, ok := fp.procedures[BackupProcedure]; !ok {
		t.Error("no Procedure for the file layer alone")
	}

	o.Config.Backups.Files.Target = ""
	o.filesReady = false
	fp.deleted = nil
	if err := o.setupBackups(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Stack urbex-backup-gitea", "Stack urbex-backup-komodo", "Stack urbex-backup-keycloak",
		"Variable " + BackupPasswordVariable, "Variable " + BackupS3SecretKeyVariable, "Procedure " + BackupProcedure} {
		if !slices.Contains(fp.deleted, want) {
			t.Errorf("deleted %v lacks %s", fp.deleted, want)
		}
	}
}

// A file layer that doesn't run leaves no backup Stack's folder in the
// GitOps repo.
func TestRemoveFileBackupFolders(t *testing.T) {
	o, _ := filesOrchestrator(t, "")
	repo := t.TempDir()
	dir := filepath.Join(repo, PlatformStacksDir, "backup-gitea")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := o.removeFileBackupFolders(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s still there", dir)
	}
}
