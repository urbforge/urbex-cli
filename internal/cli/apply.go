package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newApplyCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var repoDir string

	cmd := &cobra.Command{
		Use:   "apply <env>",
		Short: "Apply the planned infrastructure changes for an environment",
		Long: `Like 'urbex plan', but runs 'terraform apply', then installs Docker and
the Komodo Periphery agent on each service's LXC and hands the
environment over to Komodo: per service a Build (from the app's repo on
Gitea) and a Deployment, a Procedure that runs them, and a Gitea webhook
that triggers it on every push to the environment's branch - staging for
staging, main for prod. It then runs the Procedure once, so the
environment is up when apply returns.

If the environment's branch doesn't exist on Gitea yet, it is created
from the local HEAD of --repo-dir. DNS registration, Keycloak
realm/client provisioning, and Cloudflare Tunnel ingress are not yet
implemented: services are reachable on their LAN IPs, printed at the end.

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

			return o.Apply(cmd.Context(), provision.Options{
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
