package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/status"
)

func newStatusCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "status [env]",
		Short: "Show the current status of the platform or of a project environment",
		Long: `With no arguments, shows which base-service LXCs (Gitea, Komodo,
Technitium, Keycloak, observability) currently exist on Proxmox.

With an environment argument (staging or prod), shows, for each service
declared in urbex.yaml, whether urbex plan/apply has allocated it a
VMID/IP and whether its LXC currently exists on Proxmox.

DNS records, TLS certificates, and the latest deployed release are not
tracked yet - see docs/architecture.md in urbforge/urbex.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			configPath := filepath.Join(repoPath, "urbex.platform.yaml")
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				return fmt.Errorf("%s not found - run 'urbex bootstrap --gitops-repo %s' first", configPath, repoPath)
			}
			cfg, err := platform.LoadFile(configPath)
			if err != nil {
				return fmt.Errorf("loading %s: %w", configPath, err)
			}

			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			if creds.ProxmoxToken == "" {
				return fmt.Errorf("URBEX_PROXMOX_TOKEN (or proxmoxToken in ~/.urbex/credentials.yaml) is required")
			}
			client := proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure)

			if len(args) == 0 {
				return runBaseStatus(cmd, out, client, cfg)
			}
			return runProjectStatus(cmd, out, client, cfg, repoPath, manifestPath, args[0])
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest (only used when env is given)")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")

	return cmd
}

func runBaseStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config) error {
	statuses, err := status.Base(cmd.Context(), client, cfg.Proxmox.Node)
	if err != nil {
		return fmt.Errorf("checking Proxmox: %w", err)
	}

	fmt.Fprintln(out, "Base services:")
	for _, s := range statuses {
		fmt.Fprintf(out, "  %-24s %s\n", s.Hostname, presence(s.Present))
	}
	return nil
}

func runProjectStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config, repoPath, manifestPath, env string) error {
	if !provision.ValidEnvs[env] {
		return fmt.Errorf("invalid environment %q: must be one of staging, prod", env)
	}

	m, result, err := manifest.LoadTyped(manifestPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", manifestPath, err)
	}
	if m == nil {
		fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return fmt.Errorf("%s failed validation", manifestPath)
	}

	ledgerPath := filepath.Join(repoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, cfg.Proxmox.VMIDBase+100, cfg.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return fmt.Errorf("loading allocation ledger: %w", err)
	}

	statuses, err := status.Project(cmd.Context(), client, cfg.Proxmox.Node, ledger, m.Project, env, m.Services)
	if err != nil {
		return fmt.Errorf("checking Proxmox: %w", err)
	}

	fmt.Fprintf(out, "%s (%s):\n", m.Project, env)
	for _, s := range statuses {
		fmt.Fprintf(out, "  %-16s %-32s allocated=%-3s provisioned=%s\n",
			s.Service, s.Hostname, yesno(s.Allocated), yesno(s.Provisioned))
	}
	fmt.Fprintln(out, "\nDNS, TLS certificates, and the latest deployed release are not tracked yet.")
	return nil
}

func presence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

func yesno(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
