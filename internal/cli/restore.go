package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/restore"
)

func newRestoreCmd() *cobra.Command {
	var manifestPath, gitopsRepoPath, machine, from, at, storage string
	var yes bool

	cmd := &cobra.Command{
		Use:   "restore <env|platform> --machine <name> --from proxmox",
		Short: "Restore a whole machine from its Proxmox backup",
		Long: `Restores one machine of the platform or of an environment of this project
from the Proxmox layer of the backups (backups.proxmox in
urbex.platform.yaml): its newest backup on backups.proxmox.storage, or the
newest at or before --at, restored onto the same VMID. The machine is
stopped, its disks and configuration are replaced by the backup's, and it
is started again if it was running. Whatever changed on it since the
backup is lost; the databases' dumps in it are as they were at the backup.

<name> is a platform service (gitea, komodo, technitium, keycloak,
observability, tunnel) for 'platform', or an application - or its group
(lxc:) - of urbex.yaml for an environment. Only the platform's pool's
machines are restored, with the Proxmox token of ~/.urbex/credentials.yaml.

Without --yes it only says which backup it would restore.

See docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from != "proxmox" {
				return fmt.Errorf("--from %q: only proxmox is supported for now (the file layer comes with its own restore)", from)
			}
			if machine == "" {
				return fmt.Errorf("--machine is required: which machine to restore")
			}
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			cfg, err := platform.LoadFile(filepath.Join(repoPath, "urbex.platform.yaml"))
			if err != nil {
				return fmt.Errorf("loading platform config: %w", err)
			}
			if storage == "" {
				storage = cfg.Backups.Proxmox.Storage
			}
			if storage == "" {
				return fmt.Errorf("no backups storage: backups.proxmox.storage isn't set in urbex.platform.yaml (or pass --storage)")
			}
			if cfg.Proxmox.Pool == "" {
				return fmt.Errorf("proxmox.pool isn't set in urbex.platform.yaml: urbex restores only the pool's machines")
			}
			hostname, err := restoreHostname(args[0], machine, manifestPath, cfg)
			if err != nil {
				return err
			}
			when, err := restore.ParseAt(at, time.Local)
			if err != nil {
				return err
			}
			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			pve := proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure)
			return restore.Machine(cmd.Context(), pve, cmd.OutOrStdout(), restore.Options{
				Hostname:    hostname,
				Pool:        cfg.Proxmox.Pool,
				Storage:     storage,
				DiskStorage: cfg.Proxmox.StoragePool,
				At:          when,
				Yes:         yes,
			})
		},
	}

	cmd.Flags().StringVar(&machine, "machine", "", "the machine to restore: a platform service, or an application (or its group) of urbex.yaml")
	cmd.Flags().StringVar(&from, "from", "", "the backups' layer: proxmox")
	cmd.Flags().StringVar(&at, "at", "", "restore the newest backup at or before this time (\"2026-11-25 06:00\", local time, or a date); default the newest")
	cmd.Flags().StringVar(&storage, "storage", "", "the Proxmox storage holding the backups (default backups.proxmox.storage)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm: replace the machine with its backup")
	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest (for an environment)")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	return cmd
}

// restoreHostname resolves --machine to the machine's name on Proxmox:
// urbex-<service> for the platform, the project's machine for an
// environment.
func restoreHostname(target, machine, manifestPath string, cfg *platform.Config) (string, error) {
	if target == "platform" {
		var names []string
		for _, s := range bootstrap.BaseServicesFor(cfg) {
			if s.Name == machine || bootstrap.Hostname(s.Name) == machine {
				return bootstrap.Hostname(s.Name), nil
			}
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("the platform has no machine %q: one of %s", machine, strings.Join(names, ", "))
	}
	m, _, err := manifest.LoadTyped(manifestPath)
	if err != nil || m == nil {
		return "", fmt.Errorf("loading %s (an environment's machines are its applications): %v", manifestPath, err)
	}
	if !slices.Contains(m.EnvironmentList(), target) {
		return "", fmt.Errorf("%s has no environment %q: one of %s, or platform", m.Project, target, strings.Join(m.EnvironmentList(), ", "))
	}
	var names []string
	for _, svc := range m.Services {
		host := provision.MachineHostname(m.Project, svc, target)
		if svc.Group() == machine || svc.Name == machine || host == machine {
			return host, nil
		}
		if !slices.Contains(names, svc.Group()) {
			names = append(names, svc.Group())
		}
	}
	return "", fmt.Errorf("%s has no machine %q in %s: one of %s", m.Project, machine, target, strings.Join(names, ", "))
}
