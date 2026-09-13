package cli

import (
	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newApplyCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "apply <env>",
		Short: "Apply the planned infrastructure changes for an environment",
		Long: `Like 'urbex plan', but runs 'terraform apply' and then configures each
service's LXC via ansible-playbook (Docker + a docker-compose skeleton).

DNS registration, Keycloak realm/client provisioning, and Cloudflare
Tunnel ingress setup are not yet implemented - apply prints what to do
next instead.

Requires urbex.platform.yaml to already exist in --gitops-repo (created
by 'urbex bootstrap').`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, true)
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

	return cmd
}
