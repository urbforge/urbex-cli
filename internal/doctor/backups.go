package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// BackupReader is what the backup checks read with the backup token
// (proxmoxapi.Client).
type BackupReader interface {
	Permissions(ctx context.Context) (map[string]map[string]bool, error)
	PermissionsOf(ctx context.Context, userid string) (map[string]map[string]bool, error)
	StorageStatus(ctx context.Context, node, storage string) (proxmoxapi.Storage, error)
}

// The backup token's privileges (ADR-0036): on the pool, to see its
// machines - Proxmox names a machine's pool only to who has Pool.Audit on
// it - and back them up; on the storage, to write the backups and
// list them. Nothing else.
var (
	backupPoolPrivileges    = []string{"Pool.Audit", "VM.Audit", "VM.Backup"}
	backupStoragePrivileges = []string{"Datastore.AllocateSpace", "Datastore.Audit"}
)

const backupTokenDoc = "docs/reference/credentials.md#proxmox-backup-token"

// BackupChecks checks the Proxmox layer of the backups
// (urbforge/urbex-cli#121): configured or not, the backup token, its
// privileges on the pool and the storage - naming, when it lacks one,
// whether its user has it, since a privilege-separated token gets only
// what both have - and the storage. b is nil without the token.
func BackupChecks(ctx context.Context, b BackupReader, token string, cfg *platform.Config, r *Report) {
	add := func(name, status, detail, fix string) {
		r.add(Check{Group: "backups", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	if cfg == nil {
		return
	}
	bc := cfg.Backups.WithDefaults()
	p := bc.Proxmox
	if !p.Enabled() {
		add("proxmox layer", Warn, "off: no backups.proxmox.storage, so nothing backs the machines up",
			"name a Proxmox Backup Server datastore or a storage with the backup content in backups.proxmox.storage, create the backup token ("+backupTokenDoc+"), then 'urbex bootstrap'")
		return
	}
	if b == nil {
		add("backup token", Fail, "backups.proxmox.storage is "+p.Storage+" but there is no proxmoxBackupToken",
			"create it ("+backupTokenDoc+"), 'urbex-op set proxmoxBackupToken', then 'urbex bootstrap'")
		return
	}
	if !proxmoxToken.MatchString(token) {
		add("backup token", Fail, "proxmoxBackupToken isn't a whole token: it needs the token's id before its secret",
			"store it again as urbex-backup@pve!backup=<secret> ('urbex-op set proxmoxBackupToken')")
		return
	}
	perms, err := b.Permissions(ctx)
	if err != nil {
		add("backup token", Fail, "the backup token doesn't work: "+firstLine(err.Error()),
			"check proxmoxBackupToken (user@realm!name=secret) and that the token exists on the node")
		return
	}
	user := token[:strings.Index(token, "!")]
	var userPerms map[string]map[string]bool
	if up, err := b.PermissionsOf(ctx, user); err == nil {
		userPerms = up
	}
	pool := "/pool/" + cfg.Proxmox.Pool
	storage := "/storage/" + p.Storage
	var problems, fixes []string
	for _, need := range []struct {
		path  string
		privs []string
	}{{pool, backupPoolPrivileges}, {storage, backupStoragePrivileges}} {
		var tokenLacks, bothLack []string
		for _, priv := range need.privs {
			if has(perms, need.path, priv) {
				continue
			}
			if userPerms != nil && has(userPerms, need.path, priv) {
				tokenLacks = append(tokenLacks, priv)
			} else {
				bothLack = append(bothLack, priv)
			}
		}
		if len(tokenLacks) > 0 {
			problems = append(problems, fmt.Sprintf("the token lacks %s on %s, which its user %s has", strings.Join(tokenLacks, ", "), need.path, user))
			fixes = append(fixes, fmt.Sprintf("pveum acl modify %s --tokens '%s' --roles <a role with %s>", need.path, token[:strings.Index(token, "=")], strings.Join(tokenLacks, ", ")))
		}
		if len(bothLack) > 0 {
			who := "the token and its user " + user + " lack"
			if userPerms == nil {
				who = "the token lacks"
			}
			problems = append(problems, fmt.Sprintf("%s %s on %s", who, strings.Join(bothLack, ", "), need.path))
			fixes = append(fixes, fmt.Sprintf("pveum acl modify %s --users %s --roles <a role with %s> (and --tokens for a privilege-separated token)", need.path, user, strings.Join(bothLack, ", ")))
		}
	}
	if len(problems) > 0 {
		add("backup token", Fail, strings.Join(problems, "; "), "on the node, as root: "+strings.Join(fixes, "; ")+" - see "+backupTokenDoc)
		return
	}
	add("backup token", OK, "Pool.Audit, VM.Audit and VM.Backup on "+pool+", Datastore.AllocateSpace and Datastore.Audit on "+storage, "")

	s, err := b.StorageStatus(ctx, cfg.Proxmox.Node, p.Storage)
	switch {
	case err != nil:
		add("backup storage", Fail, fmt.Sprintf("%s on %s: %s", p.Storage, cfg.Proxmox.Node, firstLine(err.Error())),
			"add the storage to the node (Datacenter > Storage) - a Proxmox Backup Server on another device, or any storage with the backup content - or fix backups.proxmox.storage")
	case !s.Has("backup"):
		add("backup storage", Fail, fmt.Sprintf("%s holds %s, not backup", p.Storage, strings.Join(s.Content, ",")),
			"enable the backup content on it (Datacenter > Storage > Edit), or name another storage")
	case !s.Active:
		add("backup storage", Fail, p.Storage+" is not active on "+cfg.Proxmox.Node, "check the storage (a PBS that answers, a mounted share) in Proxmox")
	default:
		add("backup storage", OK, fmt.Sprintf("%s (%s), %d of %d GB free; every day at %s %s, %s, keep %d daily, %d weekly, %d monthly",
			p.Storage, s.Type, s.AvailGB, s.TotalGB, bc.Schedule, bc.Timezone, p.Mode, p.Keep.Daily, p.Keep.Weekly, p.Keep.Monthly), "")
	}
}

const filesBackupDoc = "docs/reference/platform-config.md#backups"

// FilesBackupChecks checks the file layer of the backups
// (urbforge/urbex-cli#122): configured or not, its password, the bucket's
// key, the share bound into the platform's LXCs - shareCommands are the
// operator's pct set commands still to run, shareErr why they couldn't be
// read - and whether the operator has stored the two secrets a rebuild
// needs off the platform.
func FilesBackupChecks(cfg *platform.Config, creds FilesCredentials, shareCommands []string, shareErr error, r *Report) {
	add := func(name, status, detail, fix string) {
		r.add(Check{Group: "backups", Name: name, Status: status, Detail: detail, Fix: fix})
	}
	if cfg == nil {
		return
	}
	b := cfg.Backups.WithDefaults()
	f := b.Files
	if !f.Enabled() {
		detail := "off: no backups.files.target, so nothing is copied off the node"
		if !b.Proxmox.Enabled() {
			detail = "off, and the Proxmox layer too: nothing is backed up"
		}
		add("file layer", Warn, detail,
			"set backups.files.target - a bucket (s3:https://<endpoint>/<bucket>) or a share (share:<name>/<folder>) - then 'urbex bootstrap' ("+filesBackupDoc+")")
		return
	}
	if creds.Password == "" {
		add("file layer", Fail, "backups.files.target is "+f.Target+" but there is no backupPassword yet",
			"run 'urbex bootstrap': it generates the password and starts the backups")
		return
	}
	if f.S3() && (creds.S3KeyID == "" || creds.S3Secret == "") {
		add("file layer", Fail, "backups.files.target is a bucket, but backupS3AccessKeyId or backupS3SecretAccessKey is missing",
			"create a key for the bucket, store both with 'urbex-op set', then 'urbex bootstrap'")
		return
	}
	switch {
	case shareErr != nil:
		add("file layer", Fail, "the share's mount points can't be read: "+firstLine(shareErr.Error()), "check proxmoxToken and that the platform's LXCs exist")
		return
	case len(shareCommands) > 0:
		add("file layer", Fail, f.Target+" isn't bound into the platform's LXCs yet, so the file layer doesn't run",
			"on the node, as root: "+strings.Join(shareCommands, "; ")+", then 'urbex bootstrap'")
		return
	}
	add("file layer", OK, fmt.Sprintf("the forge's, Komodo's and Keycloak's data to %s every day at %s %s, keep %d daily, %d weekly, %d monthly",
		f.Target, b.Schedule, b.Timezone, f.Keep.Daily, f.Keep.Weekly, f.Keep.Monthly), "")
	if creds.SecretsStored != "yes" {
		add("backup secrets", Warn, "not confirmed stored off the platform: without the age key and backupPassword the backups can't be read",
			"store both in a password manager, then 'urbex-op set backupSecretsStored' with yes")
		return
	}
	add("backup secrets", OK, "the age key and backupPassword are stored off the platform (backupSecretsStored)", "")
}

// FilesCredentials are the file layer's credentials, from credentials.yaml.
type FilesCredentials struct {
	Password, S3KeyID, S3Secret, SecretsStored string
}
