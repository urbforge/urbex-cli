package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newPlanCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath, repoDir string

	cmd := &cobra.Command{
		Use:   "plan <env>",
		Short: "Compute the infrastructure changes needed for an environment",
		Long: `Reads urbex.yaml, allocates a VMID/IP for each declared service's LXC
(one per service per environment), materializes the Terraform module for
this project+env into the GitOps repo, and runs 'terraform plan'. Does
not apply anything or run Ansible - see 'urbex apply' for that.

Like apply, it refuses an urbex.yaml that isn't committed in the git
repository at --repo-dir while the project has no main branch on Forgejo.

Requires urbex.platform.yaml to already exist in --gitops-repo (created
by 'urbex bootstrap').`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, repoDir, true)
			if err != nil {
				return err
			}
			// What apply checks first, so that plan says it too.
			if err := checkProjectRepo(cmd, o, repoDir, manifestPath); err != nil {
				return err
			}

			return o.Plan(cmd.Context(), provision.Options{
				Env:            args[0],
				GitOpsRepoPath: repoPath,
			})
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo, which urbex.yaml must be committed in until the project is on Forgejo")

	return cmd
}
