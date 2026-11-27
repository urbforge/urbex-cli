package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/urbforge/urbex-cli/internal/komodo"
	platformassets "github.com/urbforge/urbex-cli/platform"
)

// The backups' pieces in Komodo (ADR-0036, urbforge/urbex-cli#121): the
// Action that dumps the databases and backs the machines up with vzdump,
// and the Procedure that runs it every day. The Action reads its
// configuration and the backup token from two variables.
const (
	BackupAction         = "urbex-backup"
	BackupProcedure      = "urbex-backup"
	BackupConfigVariable = "URBEX_BACKUP_CONFIG"
	BackupTokenVariable  = "URBEX_PROXMOX_BACKUP_TOKEN"
	// DumpPostgresTag marks the Stacks of the projects' PostgreSQL
	// servers, which the Action dumps before the backup
	// (platform/komodo/backup_logic.ts).
	DumpPostgresTag = "dump-postgres"
)

// ErrNoBackupToken is a Proxmox layer configured without its token.
var ErrNoBackupToken = fmt.Errorf("backups.proxmox.storage is set but there is no proxmoxBackupToken: create the backup token (docs/reference/credentials.md#proxmox-backup-token), store it with 'urbex-op set proxmoxBackupToken', then run 'urbex bootstrap' again")

// BackupScript is the backup Action's code: its logic, without the
// exports that only its tests need, then what drives it from Komodo.
func BackupScript() (string, error) {
	return komodoScript("komodo/backup_logic.ts", "komodo/backup.ts")
}

// komodoScript joins an Action's logic, without its exports, and its
// driver.
func komodoScript(logicPath, driverPath string) (string, error) {
	logic, err := platformassets.Komodo.ReadFile(logicPath)
	if err != nil {
		return "", err
	}
	driver, err := platformassets.Komodo.ReadFile(driverPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(logic), "\n") {
		b.WriteString(strings.TrimPrefix(line, "export "))
	}
	b.WriteString("\n")
	b.Write(driver)
	return b.String(), nil
}

// backupConfig is URBEX_BACKUP_CONFIG: BackupConfig in backup_logic.ts.
type backupConfig struct {
	API           string            `json:"api"`
	Insecure      bool              `json:"insecure"`
	Node          string            `json:"node"`
	Pool          string            `json:"pool"`
	Storage       string            `json:"storage"`
	Machines      []string          `json:"machines"`
	Mode          string            `json:"mode"`
	Modes         map[string]string `json:"modes"`
	Keep          backupKeep        `json:"keep"`
	PlatformHosts []string          `json:"platform_hosts"`
	// ProxmoxOn says the Proxmox layer runs; Files is the file layer's
	// configuration, nil when it doesn't run.
	ProxmoxOn bool         `json:"proxmox"`
	Files     *filesConfig `json:"files"`
}

// filesConfig is BackupConfig.files in backup_logic.ts.
type filesConfig struct {
	Stacks []fileStack `json:"stacks"`
	Keep   backupKeep  `json:"keep"`
}

// fileStack is a backup Stack: where restic runs, the host it names its
// snapshots after, and what it copies.
type fileStack struct {
	Stack string   `json:"stack"`
	Host  string   `json:"host"`
	Paths []string `json:"paths"`
	// Credentials: the Stack writes credentials.yaml's backup copy to
	// BackupCredentialsDir, one of Paths, before restic runs.
	Credentials bool `json:"credentials"`
}

type backupKeep struct {
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// BackupConfigJSON is the backup Action's configuration, from the
// platform config.
func (o *Orchestrator) BackupConfigJSON() (string, error) {
	b := o.Config.Backups.WithDefaults()
	p := b.Proxmox
	modes := p.Modes
	if modes == nil {
		modes = map[string]string{}
	}
	// Every machine the platform may have - the tunnel's too, which a
	// platform without Cloudflare no longer uses - is the platform's.
	var hosts []string
	for _, svc := range append(append([]Service{}, BaseServices...), TunnelService) {
		hosts = append(hosts, Hostname(svc.Name))
	}
	data, err := json.Marshal(backupConfig{
		API:           strings.TrimRight(o.Config.Proxmox.APIURL, "/"),
		Insecure:      o.Config.Proxmox.Insecure,
		Node:          o.Config.Proxmox.Node,
		Pool:          o.Config.Proxmox.Pool,
		Storage:       p.Storage,
		Machines:      p.Machines,
		Mode:          p.Mode,
		Modes:         modes,
		Keep:          backupKeep{Daily: p.Keep.Daily, Weekly: p.Keep.Weekly, Monthly: p.Keep.Monthly},
		PlatformHosts: hosts,
		ProxmoxOn:     p.Enabled(),
		Files:         o.filesConfig(),
	})
	if err != nil {
		return "", err
	}
	// The Action reads it from a raw template string: no backquote may
	// end it early.
	if strings.ContainsAny(string(data), "`") {
		return "", fmt.Errorf("the backups' configuration can't contain a backquote")
	}
	return string(data), nil
}

// BackupKomodo is what setupBackups needs of Komodo.
type BackupKomodo interface {
	EnsureVariable(ctx context.Context, name, value string, secret bool) error
	DeleteVariable(ctx context.Context, name string) error
	EnsureAction(ctx context.Context, name string, cfg komodo.ActionConfig) error
	EnsureProcedure(ctx context.Context, name string, cfg komodo.ProcedureConfig) error
	DeleteResource(ctx context.Context, kind, name string) error
	TagResource(ctx context.Context, kind, name string, tags ...string) error
}

// setupBackups sets up the backups in Komodo when a layer is configured
// - the variables, the Action, the Procedure on backups.schedule - and
// removes what a layer that is off left: its variables (the Proxmox
// token first: the platform never keeps a token it doesn't use) and the
// file layer's Stacks; with both layers off, the Action and the
// Procedure too.
func (o *Orchestrator) setupBackups(ctx context.Context, k BackupKomodo) error {
	b := o.Config.Backups.WithDefaults()
	proxmoxOn, filesOn := b.Proxmox.Enabled(), o.filesReady
	if !proxmoxOn {
		if err := k.DeleteVariable(ctx, BackupTokenVariable); err != nil {
			return err
		}
	}
	if !filesOn {
		// Their own compose project: removing them never touches the
		// service next to them.
		for _, svc := range FileBackupServices {
			if err := k.DeleteResource(ctx, "Stack", FileBackupStackName(svc)); err != nil {
				return err
			}
		}
	}
	if !filesOn || !b.Files.S3() {
		for _, v := range []string{BackupS3KeyIDVariable, BackupS3SecretKeyVariable} {
			if err := k.DeleteVariable(ctx, v); err != nil {
				return err
			}
		}
	}
	if !filesOn {
		for _, v := range []string{BackupRepositoryVariable, BackupPasswordVariable, BackupCredentialsVariable} {
			if err := k.DeleteVariable(ctx, v); err != nil {
				return err
			}
		}
	}
	if !proxmoxOn && !filesOn {
		if err := k.DeleteResource(ctx, "Procedure", BackupProcedure); err != nil {
			return err
		}
		if err := k.DeleteResource(ctx, "Action", BackupAction); err != nil {
			return err
		}
		return k.DeleteVariable(ctx, BackupConfigVariable)
	}
	if proxmoxOn {
		if o.Credentials.ProxmoxBackupToken == "" {
			return ErrNoBackupToken
		}
		if err := k.EnsureVariable(ctx, BackupTokenVariable, o.Credentials.ProxmoxBackupToken, true); err != nil {
			return err
		}
	}
	if filesOn {
		creds, err := o.Credentials.BackupCopy()
		if err != nil {
			return err
		}
		vars := []komodoVariable{
			{BackupRepositoryVariable, o.backupRepository(), false},
			{BackupPasswordVariable, o.Credentials.BackupPassword, true},
			{BackupCredentialsVariable, base64.StdEncoding.EncodeToString(creds), true},
		}
		if b.Files.S3() {
			vars = append(vars,
				komodoVariable{BackupS3KeyIDVariable, o.Credentials.BackupS3AccessKeyID, true},
				komodoVariable{BackupS3SecretKeyVariable, o.Credentials.BackupS3SecretAccessKey, true})
		}
		for _, v := range vars {
			if err := k.EnsureVariable(ctx, v.name, v.value, v.secret); err != nil {
				return err
			}
		}
	}
	cfg, err := o.BackupConfigJSON()
	if err != nil {
		return err
	}
	if err := k.EnsureVariable(ctx, BackupConfigVariable, cfg, false); err != nil {
		return err
	}
	script, err := BackupScript()
	if err != nil {
		return err
	}
	if err := k.EnsureAction(ctx, BackupAction, komodo.ActionConfig{FileContents: script}); err != nil {
		return err
	}
	if err := k.EnsureProcedure(ctx, BackupProcedure, komodo.ProcedureConfig{
		ScheduleFormat:   "Cron",
		Schedule:         b.Cron(),
		ScheduleTimezone: b.Timezone,
		ScheduleEnabled:  true,
		Stages: []komodo.Stage{
			komodo.NewStage("Dumps, then the backups", "RunAction", "action", []string{BackupAction}),
		},
	}); err != nil {
		return err
	}
	if err := k.TagResource(ctx, "Action", BackupAction, PlatformTag); err != nil {
		return err
	}
	if err := k.TagResource(ctx, "Procedure", BackupProcedure, PlatformTag); err != nil {
		return err
	}
	if proxmoxOn {
		fmt.Fprintf(o.Out, "Backups: every day at %s (%s), the pool's %s machines to %s.\n",
			b.Schedule, b.Timezone, strings.Join(b.Proxmox.Machines, " and "), b.Proxmox.Storage)
	}
	if filesOn {
		fmt.Fprintf(o.Out, "Backups: every day at %s (%s), the forge's, Komodo's and Keycloak's data with restic to %s.\n",
			b.Schedule, b.Timezone, b.Files.Target)
	}
	return nil
}

// komodoVariable is a Komodo variable setupBackups keeps.
type komodoVariable struct {
	name, value string
	secret      bool
}

// filesConfig is the file layer's part of the Action's configuration, or
// nil when it doesn't run. Each backup Stack mounts its data under /data
// (platform/stacks/backup-*).
func (o *Orchestrator) filesConfig() *filesConfig {
	if !o.filesReady {
		return nil
	}
	k := o.Config.Backups.WithDefaults().Files.Keep
	fc := &filesConfig{Keep: backupKeep{Daily: k.Daily, Weekly: k.Weekly, Monthly: k.Monthly}}
	for _, svc := range FileBackupServices {
		st := fileStack{Stack: FileBackupStackName(svc), Host: Hostname(svc), Paths: []string{"/data/" + svc}}
		if svc == BackupCredentialsService {
			st.Paths, st.Credentials = append(st.Paths, BackupCredentialsDir), true
		}
		fc.Stacks = append(fc.Stacks, st)
	}
	return fc
}
