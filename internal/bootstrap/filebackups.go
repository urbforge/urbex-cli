package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// The file layer of the backups (ADR-0036, point 4; urbforge/urbex-cli#122):
// restic copies of the platform's data, into one repository per platform.
//
// A Stack urbex-backup-<service> on each base service's LXC with data -
// the forge, Komodo, Keycloak - holds restic next to that data; the backup
// Action runs it after the dumps. The repository, its password and the
// bucket's key are Komodo variables, which reach the Stacks through their
// environment: nothing of them is in the GitOps repo.
const (
	FileBackupStackPrefix = "urbex-backup-"
	// FileBackupProject is the backup Stacks' compose project: their own,
	// so that removing one never touches the service next to it.
	FileBackupProject = "urbex-backup"

	BackupRepositoryVariable  = "URBEX_BACKUP_REPOSITORY"
	BackupPasswordVariable    = "URBEX_BACKUP_PASSWORD"
	BackupS3KeyIDVariable     = "URBEX_BACKUP_S3_ACCESS_KEY_ID"
	BackupS3SecretKeyVariable = "URBEX_BACKUP_S3_SECRET_ACCESS_KEY"
	// BackupCredentialsVariable is credentials.yaml's backup copy
	// (credentials.BackupCopy), base64: the Komodo backup Stack writes it
	// to BackupCredentialsDir before restic runs.
	BackupCredentialsVariable = "URBEX_BACKUP_CREDENTIALS"
	BackupCredentialsDir      = "/data/credentials"
	// BackupCredentialsService is the base service whose backup Stack
	// keeps the copy: Komodo, which holds it already.
	BackupCredentialsService = "komodo"

	// BackupShareMount is where a share target (share:<name>) is bound
	// into each of those LXCs, and the backup Stacks mount it at /repo.
	BackupShareMount = "/mnt/urbex-backup"
)

// FileBackupServices are the base services whose data the file layer
// copies, in the order the Action backs them up.
var FileBackupServices = []string{"gitea", "komodo", "keycloak"}

// FileBackupStackName is the backup Stack on a base service's LXC.
func FileBackupStackName(service string) string { return FileBackupStackPrefix + service }

// ErrNoBackupS3Key is a bucket configured without its access key.
var ErrNoBackupS3Key = fmt.Errorf("backups.files.target is a bucket but the credentials have no backupS3AccessKeyId and backupS3SecretAccessKey: create a key for the bucket, store both with 'urbex-op set', then run 'urbex bootstrap' again")

// backupRepository is RESTIC_REPOSITORY: the bucket as configured, or the
// folder of the share inside the backup Stacks' /repo.
func (o *Orchestrator) backupRepository() string {
	f := o.Config.Backups.Files
	if f.S3() {
		return f.Target
	}
	_, folder := f.Share()
	return path.Join("/repo", folder)
}

// fileBackupStacks are the backup Stacks, when the file layer is on.
func (o *Orchestrator) fileBackupStacks() []PlatformStack {
	f := o.Config.Backups.Files
	if !f.Enabled() {
		return nil
	}
	env := "RESTIC_REPOSITORY=[[" + BackupRepositoryVariable + "]]\n" +
		"RESTIC_PASSWORD=[[" + BackupPasswordVariable + "]]\n"
	if f.S3() {
		env += "AWS_ACCESS_KEY_ID=[[" + BackupS3KeyIDVariable + "]]\n" +
			"AWS_SECRET_ACCESS_KEY=[[" + BackupS3SecretKeyVariable + "]]\n"
	} else {
		env += "URBEX_BACKUP_REPO=" + BackupShareMount + "\n"
	}
	var stacks []PlatformStack
	for _, svc := range FileBackupServices {
		stackEnv := env
		if svc == BackupCredentialsService {
			stackEnv += "URBEX_CREDENTIALS=[[" + BackupCredentialsVariable + "]]\n"
		}
		stacks = append(stacks, PlatformStack{
			Name: FileBackupStackName(svc), Dir: "backup-" + svc, Server: Hostname(svc),
			ProjectName: FileBackupProject, Config: map[string]string{}, Environment: stackEnv,
		})
	}
	return stacks
}

// ensureBackupPassword generates the restic repository's password the
// first time the file layer is on, keeps it in credentials.yaml - where
// a teardown leaves it - and prints it once: the operator must store it
// off the platform, with the age key.
func (o *Orchestrator) ensureBackupPassword() error {
	if !o.Config.Backups.Files.Enabled() || o.Credentials.BackupPassword != "" {
		return nil
	}
	password, err := credentials.RandomSecret()
	if err != nil {
		return err
	}
	if err := o.Credentials.Update(func(c *credentials.Credentials) { c.BackupPassword = password }); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, `
The backups' password, which encrypts them (shown once):

    %s

Store it off the platform, in a password manager, with the age key: without
it the backups can't be read, and the platform can't be rebuilt from them.
It is also in credentials.yaml (backupPassword). Once both are stored, run
'urbex-op set backupSecretsStored' with "yes" - urbex doctor warns until then.

`, password)
	return nil
}

// MountPointReader reads an LXC's mount points: what a share target needs
// of the Proxmox API.
type MountPointReader interface {
	MountPoints(ctx context.Context, node string, vmid int) ([]proxmoxapi.MountPoint, error)
}

// BackupShareCommands are the operator's pct set commands that bind a
// share target into the LXCs of FileBackupServices, for those that don't
// have it yet; none for a bucket.
func BackupShareCommands(ctx context.Context, cfg ShareConfig, mp MountPointReader) ([]string, error) {
	if cfg.SharePath == "" {
		return nil, nil
	}
	var commands []string
	for _, svc := range FileBackupServices {
		vmid := cfg.VMIDs[svc]
		points, err := mp.MountPoints(ctx, cfg.Node, vmid)
		if err != nil {
			return nil, fmt.Errorf("reading the mount points of %s: %w", Hostname(svc), err)
		}
		bound, taken := false, map[int]bool{}
		for _, p := range points {
			taken[p.Index()] = true
			if path.Clean(p.Path) == BackupShareMount && path.Clean(p.Volume) == path.Clean(cfg.SharePath) {
				bound = true
			}
		}
		if bound {
			continue
		}
		key := 0
		for taken[key] {
			key++
		}
		commands = append(commands, fmt.Sprintf("pct set %d -mp%d %s,mp=%s", vmid, key, cfg.SharePath, BackupShareMount))
	}
	return commands, nil
}

// ShareConfig is what BackupShareCommands needs: the node, the share's
// folder on it ("" for a bucket), and the base services' VMIDs.
type ShareConfig struct {
	Node      string
	SharePath string
	VMIDs     map[string]int
}

// BackupShareConfig is ShareConfig for a platform.
func BackupShareConfig(cfg *platform.Config) ShareConfig {
	sc := ShareConfig{Node: cfg.Proxmox.Node, VMIDs: map[string]int{}}
	if name, _ := cfg.Backups.Files.Share(); name != "" {
		sc.SharePath = cfg.Storage.Shares[name].Path
	}
	for i, svc := range BaseServices {
		sc.VMIDs[svc.Name] = cfg.Proxmox.VMIDBase + i
	}
	return sc
}

// fileBackupsReady reports whether the file layer can run: on, with its
// credentials, and with a share target bound into every LXC. When it
// can't yet, it prints what the operator must do.
func (o *Orchestrator) fileBackupsReady(ctx context.Context) (bool, error) {
	f := o.Config.Backups.Files
	if !f.Enabled() {
		return false, nil
	}
	if f.S3() && (o.Credentials.BackupS3AccessKeyID == "" || o.Credentials.BackupS3SecretAccessKey == "") {
		return false, ErrNoBackupS3Key
	}
	if f.S3() {
		return true, nil
	}
	mp, ok := o.Proxmox.(MountPointReader)
	if !ok {
		return false, fmt.Errorf("backups.files.target %s: checking the share needs the Proxmox API", f.Target)
	}
	commands, err := BackupShareCommands(ctx, BackupShareConfig(o.Config), mp)
	if err != nil || len(commands) == 0 {
		return err == nil, err
	}
	fmt.Fprintf(o.Out, "The backups' share isn't bound into the platform's LXCs yet, and binding a folder of the node needs root@pam: on the node, as root, run\n  %s\nthen 'urbex bootstrap' again. Until then the file layer doesn't run.\n", strings.Join(commands, "\n  "))
	return false, nil
}

// removeFileBackupFolders removes the backup Stacks' folders from the
// GitOps repo when the file layer doesn't run, so none stays behind.
func (o *Orchestrator) removeFileBackupFolders(gitopsRepoPath string) error {
	if o.filesReady {
		return nil
	}
	for _, svc := range FileBackupServices {
		if err := os.RemoveAll(filepath.Join(gitopsRepoPath, PlatformStacksDir, "backup-"+svc)); err != nil {
			return err
		}
	}
	return nil
}
