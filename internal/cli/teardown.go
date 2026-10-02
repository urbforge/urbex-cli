package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

func newTeardownCmd() *cobra.Command {
	var gitopsRepoPath string
	var yes bool

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Destroy the platform 'urbex bootstrap' created",
		Long: `Destroys the base-service LXCs 'urbex bootstrap' created on Proxmox -
Gitea, Komodo, Technitium, Keycloak, observability - with their disks,
and with them everything they hold: every repository and image on Gitea
(the project repos too, unless they also live elsewhere), Komodo's and
Keycloak's configuration, Grafana's dashboards.

It refuses while any project environment exists: run 'urbex destroy
<env>' in each project first. Without --yes it only says what it would
destroy.

The credentials bootstrap generated are removed from
~/.urbex/credentials.yaml (the Proxmox token, the age key, and the other
ones you provided are kept). The GitOps repo's working copy is kept and
committed locally: it is the only copy left, and 'urbex bootstrap'
creates a new platform from it and pushes it to the new Gitea.

See docs/reference/cli.md in urbforge/urbex.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			cfg, err := platform.LoadFile(repoPath + "/urbex.platform.yaml")
			if err != nil {
				return fmt.Errorf("loading platform config: %w", err)
			}
			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			o := &bootstrap.Orchestrator{
				Config:      cfg,
				Credentials: creds,
				Proxmox:     proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure),
				Runner:      bootstrap.ExecRunner{Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()},
				Out:         cmd.OutOrStdout(),
			}
			return o.Teardown(cmd.Context(), bootstrap.TeardownOptions{GitOpsRepoPath: repoPath, Yes: yes})
		},
	}

	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm: destroy the platform and everything on it")
	return cmd
}
