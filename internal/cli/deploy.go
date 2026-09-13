package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newDeployCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "deploy <env>",
		Short: "Redeploy already-provisioned services for an environment",
		Long: `Re-runs Ansible against service LXCs 'urbex apply' already provisioned,
to pick up urbex.yaml changes (env vars, port) - without touching
Terraform/infrastructure. Every declared service must already have been
applied; deploy itself never provisions a new LXC (use 'urbex apply' for
that).

This runs ansible-playbook directly against the LXCs over SSH - it does
not yet trigger or go through Komodo's GitOps-driven reconciliation, and
building/pushing a real container image for the service (as opposed to
the placeholder in the compose file) is also not yet implemented. See
docs/decisions/0004-gitops-gitea-komodo.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			// deploy only runs Ansible over SSH - no Proxmox API token needed.
			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, false)
			if err != nil {
				return err
			}

			return o.Deploy(cmd.Context(), provision.Options{
				Env:            args[0],
				GitOpsRepoPath: repoPath,
			})
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")

	return cmd
}
