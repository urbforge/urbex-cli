package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var allowMajor bool

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote staging's versions to production",
		Long: `Sets production to what staging runs, for the whole stack:

  - the services built from source get the release staging runs: staging
    normally runs a commit of main, so promote looks up the release
    tagged on that commit (release it first with 'urbex release');
  - the applications from published images get the very digests staging
    runs - so an update is tried on staging first. A new major version
    of an application with volumes needs --allow-major.

It writes them into each service's version file under environments/prod
in the GitOps repo, and pushes. Komodo deploys from there; promote then
waits until production runs it.

Production gets the very images staging ran: nothing is rebuilt. Both
environments must already have been applied; a stack with only prod
(environments: [prod]) has nothing to promote. See docs/reference/cli.md
in urbforge/urbex.`,
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
			changed, err := p.Promote(cmd.Context(), repoPath, allowMajor)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to promote: production already runs what staging runs.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&allowMajor, "allow-major", false, "allow a new major version of an application from an image that has volumes")
	return cmd
}
