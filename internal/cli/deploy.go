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
		Short: "Build and deploy an environment's branch now",
		Long: `Has Komodo build and deploy the head of the environment's branch now -
what merging into that branch (staging, or main for prod) does on its
own - after re-syncing the environment's Komodo resources from
urbex.yaml. That makes it the way to roll out manifest changes (env
vars, port, healthcheck), which a merge alone does not apply.

Touches neither Terraform nor the LXCs: every declared service must
already have been applied ('urbex apply'). See
docs/decisions/0019-branch-environments-komodo-rollouts.md in
urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			// deploy only talks to Gitea and Komodo - no Proxmox API token needed.
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
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo, used to create the environment's branch on Gitea if it is missing")

	return cmd
}
