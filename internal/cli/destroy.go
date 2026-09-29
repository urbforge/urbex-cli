package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newDestroyCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "destroy <env>",
		Short: "Remove an environment's infrastructure",
		Long: `Runs 'terraform destroy' for every service of this project that 'urbex
apply' already provisioned for the given environment, then removes their
entries from the allocation ledger. Services that were never applied are
silently skipped - there's nothing to destroy for them; if none were
applied, this is a no-op.

Only destroys project-service infrastructure - base/platform services
created by 'urbex bootstrap' are out of scope.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", true)
			if err != nil {
				return err
			}

			return o.Destroy(cmd.Context(), provision.Options{
				Env:            args[0],
				GitOpsRepoPath: repoPath,
			})
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")

	return cmd
}
