package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newDestroyCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var deleteData bool

	cmd := &cobra.Command{
		Use:   "destroy <env>",
		Short: "Remove an environment's infrastructure",
		Long: `Removes an environment of this project: takes its Komodo Stacks down and
deletes them, removes each service's internal DNS record from
Technitium and, with Cloudflare, its public route and record, removes
the project's folder from that environment in the GitOps repo, runs
'terraform destroy' for every service 'urbex apply' provisioned there,
deletes their Komodo Servers, and removes their entries from the
allocation ledger. When the project has no environment left, its Komodo
Builds and release webhook go too; its repo and images stay on Gitea.
Services that were never applied are skipped; if none were, this is a
no-op.

An application's named volumes go with its LXC: an environment that has
any is destroyed only with --delete-data, and urbex names them
otherwise. Shares are never touched.

Only destroys project-service infrastructure - base/platform services
created by 'urbex bootstrap' are out of scope. See docs/reference/cli.md
in urbforge/urbex.`,
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
				DeleteData:     deleteData,
			})
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "delete the named volumes of the environment's applications with their LXCs")

	return cmd
}
