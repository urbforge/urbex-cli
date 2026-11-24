package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
)

func backupOrchestrator(storage, token string) *Orchestrator {
	cfg := &platform.Config{Domain: "example.com", Proxmox: platform.ProxmoxConfig{
		APIURL: "https://192.168.1.10:8006/api2/json/", Insecure: true, Node: "pve", Pool: "urbex"}}
	cfg.Backups.Proxmox.Storage = storage
	cfg.Backups.Proxmox.Modes = map[string]string{"urbex-gitea": "stop"}
	return &Orchestrator{Config: cfg, Credentials: &credentials.Credentials{ProxmoxBackupToken: token}, Out: io.Discard}
}

// With a storage and the token: the token as a secret variable, the
// configuration, the Action and the daily Procedure, tagged platform.
func TestSetupBackups_SchedulesTheBackup(t *testing.T) {
	fp := &fakePlatform{}
	k := &fakeKomodo{fp}
	if err := backupOrchestrator("pbs", "urbex-backup@pve!backup=s3cret").setupBackups(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if got := fp.variables[BackupTokenVariable]; got != "secret:urbex-backup@pve!backup=s3cret" {
		t.Errorf("token variable = %q, want it secret", got)
	}
	var cfg backupConfig
	if err := json.Unmarshal([]byte(fp.variables[BackupConfigVariable]), &cfg); err != nil {
		t.Fatalf("config variable: %v", err)
	}
	if cfg.API != "https://192.168.1.10:8006/api2/json" || !cfg.Insecure || cfg.Node != "pve" || cfg.Pool != "urbex" ||
		cfg.Storage != "pbs" || cfg.Mode != "snapshot" || cfg.Modes["urbex-gitea"] != "stop" ||
		cfg.Keep != (backupKeep{7, 4, 6}) || strings.Join(cfg.Machines, ",") != "platform,projects" {
		t.Errorf("config = %+v", cfg)
	}
	for _, host := range []string{"urbex-gitea", "urbex-komodo", "urbex-keycloak", "urbex-technitium", "urbex-observability", "urbex-tunnel"} {
		if !slices.Contains(cfg.PlatformHosts, host) {
			t.Errorf("platform hosts %v lack %s", cfg.PlatformHosts, host)
		}
	}
	action := fp.actions[BackupAction].FileContents
	if !strings.Contains(action, "function selectMachines(") || strings.Contains(action, "\nexport ") || !strings.Contains(action, "await main();") {
		t.Errorf("the Action isn't the logic without exports, then the driver")
	}
	proc := fp.procedures[BackupProcedure]
	if !proc.ScheduleEnabled || proc.Schedule != "0 0 3 * * *" || proc.ScheduleTimezone != "Etc/UTC" || proc.WebhookEnabled {
		t.Errorf("procedure schedule = %+v", proc)
	}
	if len(proc.Stages) != 1 || proc.Stages[0].Executions[0].Execution.Type != "RunAction" ||
		proc.Stages[0].Executions[0].Execution.Params["action"] != BackupAction {
		t.Errorf("procedure stages = %+v", proc.Stages)
	}
	for _, r := range []string{"Action " + BackupAction, "Procedure " + BackupProcedure} {
		if !slices.Equal(fp.tags[r], []string{PlatformTag}) {
			t.Errorf("%s tags = %v", r, fp.tags[r])
		}
	}
}

// Without a storage, everything goes - the token first among the
// variables: the platform keeps no Proxmox token it doesn't use.
func TestSetupBackups_OffRemovesEverything(t *testing.T) {
	fp := &fakePlatform{}
	k := &fakeKomodo{fp}
	o := backupOrchestrator("pbs", "urbex-backup@pve!backup=s3cret")
	if err := o.setupBackups(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	o.Config.Backups.Proxmox.Storage = ""
	if err := o.setupBackups(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	want := []string{"Procedure " + BackupProcedure, "Action " + BackupAction, "Variable " + BackupTokenVariable, "Variable " + BackupConfigVariable}
	if !slices.Equal(fp.deleted, want) {
		t.Errorf("deleted = %v, want %v", fp.deleted, want)
	}
	if _, ok := fp.variables[BackupTokenVariable]; ok {
		t.Error("the token is still in Komodo")
	}
}

func TestSetupBackups_StorageWithoutTokenFails(t *testing.T) {
	fp := &fakePlatform{}
	err := backupOrchestrator("pbs", "").setupBackups(context.Background(), &fakeKomodo{fp})
	if !errors.Is(err, ErrNoBackupToken) {
		t.Fatalf("err = %v, want ErrNoBackupToken", err)
	}
	if len(fp.variables)+len(fp.actions)+len(fp.procedures) != 0 {
		t.Error("something was set up without the token")
	}
}
