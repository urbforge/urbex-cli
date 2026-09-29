package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newDeployCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var repoDir string

	cmd := &cobra.Command{
		Use:   "deploy <env>",
		Short: "Redeploy already-provisioned services for an environment",
		Long: `Builds every service's image from the app repo's HEAD commit (reusing
it if that commit was already built) and redeploys it to the LXCs
'urbex apply' already provisioned, also picking up urbex.yaml changes
(env vars, port) - without touching Terraform/infrastructure. Every
declared service must already have been applied; deploy itself never
provisions a new LXC (use 'urbex apply' for that). Only committed code is
deployed.

Images are built by Komodo and pulled from Gitea's registry; the rollout
itself runs ansible-playbook over SSH (Komodo-driven rollouts and
push-to-deploy are not implemented yet). See
docs/decisions/0018-image-build-komodo-gitea-registry.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			// deploy only runs Ansible over SSH - no Proxmox API token needed.
			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, repoDir, false)
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
