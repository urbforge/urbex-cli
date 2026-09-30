package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote staging's versions to production",
		Long: `Sets production to the versions staging is set to, by copying each
service's version from environments/staging to environments/prod in the
GitOps repo and pushing. Komodo deploys from there; promote then waits
until production runs them.

Production gets the very images staging ran: nothing is rebuilt. Both
environments must already have been applied. See
docs/decisions/0020-trunk-releases-gitops-environments.md in
urbforge/urbex.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}
			changed, err := p.Promote(cmd.Context(), repoPath)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to promote: production already runs staging's versions.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	return cmd
}
