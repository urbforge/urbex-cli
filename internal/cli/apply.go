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
		Long: `Like 'urbex plan', but runs 'terraform apply', then installs Docker,
the Komodo Periphery agent, and sops on each service's LXC, and sets up:

  - the project: its repo on Gitea (creating main from the local HEAD of
    --repo-dir if Gitea doesn't have it), a Komodo Build per service,
    and the webhook that has Komodo build every push to main and publish
    every release tag (vX.Y.Z);
  - the environment: each service's folder in the GitOps repo
    (environments/<env>/<project>/<service>/: compose file,
    configuration, secrets, version) and the Komodo Stack that deploys
    it.

Staging follows main: apply builds main's head and deploys it, and every
later push to main is deployed there too. Production starts empty until
'urbex promote' (or 'urbex deploy prod') gives it a release.
Re-applying an environment that runs something redeploys it.

DNS registration, Keycloak realm/client provisioning, and Cloudflare
Tunnel ingress are not yet implemented: services are reachable on their
LAN IPs, printed at the end.

Requires urbex.platform.yaml to already exist in --gitops-repo (created
by 'urbex bootstrap'). See docs/reference/cli.md in urbforge/urbex.`,
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
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo, used to create the project's main branch on Gitea if it is missing")

	return cmd
}
